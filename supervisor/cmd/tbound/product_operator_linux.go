//go:build linux

package main

import (
 "context"
 "encoding/json"
 "errors"
 "flag"
 "fmt"
 "io"
 "io/fs"
 "os"
 "os/exec"
 "path/filepath"
 "strconv"
 "strings"
 "syscall"

 "golang.org/x/sys/unix"
 "tbound/supervisor/internal/audit"
 "tbound/supervisor/internal/gate"
 "tbound/supervisor/internal/productsession"
 "tbound/supervisor/internal/productsetup"
)

var productOperatorEUID = os.Geteuid
var productOperatorStopped = checkProductOperatorStopped
var productOperatorDelegate = delegateProductOperator

func runProductOperator(args []string,out,stderr io.Writer)error{
 flags:=flag.NewFlagSet("product turns resolve",flag.ContinueOnError);flags.SetOutput(stderr)
 turn:=flags.String("turn","","unknown turn ID");reason:=flags.String("reason","","operator reason (1-256 UTF-8 bytes)");dry:=flags.Bool("dry-run",false,"check evidence without writing")
 if err:=flags.Parse(args);err!=nil{return adminRefusal("%v",err)}
 if flags.NArg()!=0 || *turn=="" {return adminRefusal("usage: tbound product turns resolve --turn ID --reason TEXT [--dry-run]")}
 if err:=productsession.ValidateOperatorReason(*reason);err!=nil{return adminRefusal("%v",err)}
 if productOperatorEUID()!=0{return adminRefusal("operator resolution must run as root, including --dry-run")}
 if err:=productOperatorStopped();err!=nil{return adminRefusal("worker exclusion: %v",err)}
 return productOperatorDelegate(*turn,*reason,*dry,out,stderr)
}

// Require a stopped service and an empty cgroup tree, including delegated descendants.
func checkProductOperatorStopped()error{
 data,err:=exec.Command("systemctl","show",productsetup.UnitName,"--property=LoadState","--property=ActiveState","--property=MainPID","--property=ControlGroup").Output()
 if err!=nil{return fmt.Errorf("cannot inspect %s: %w",productsetup.UnitName,err)}
 fields:=map[string]string{}
 for _,line:=range strings.Split(strings.TrimSpace(string(data)),"\n"){key,value,ok:=strings.Cut(line,"=");if ok{fields[key]=value}}
 if fields["LoadState"]!="loaded" || fields["ActiveState"]!="inactive" || fields["MainPID"]!="0" {return errors.New("stop tbound-product.service before resolving a turn")}
 scope:="/sys/fs/cgroup/system.slice/"+productsetup.UnitName
 if group:=fields["ControlGroup"];group!="" && group!="/system.slice/"+productsetup.UnitName{return errors.New("service has an unrecognized command/cgroup scope")}
 var stat unix.Statfs_t
 if err:=unix.Statfs("/sys/fs/cgroup",&stat);err!=nil || stat.Type!=unix.CGROUP2_SUPER_MAGIC{return errors.New("cannot establish the cgroup v2 process inventory")}
 if err:=productOperatorEmptyCgroup(scope);err!=nil{return err}
 // Catch a manually launched supervisor outside the installed service scope too.
 account,err:=productAdminLookupUser(productsetup.ServiceUser);if err!=nil{return err}
 uid,err:=strconv.ParseUint(account.Uid,10,32);if err!=nil || uid==0{return errors.New("service account must have a non-root numeric UID")}
 entries,err:=os.ReadDir("/proc");if err!=nil{return err}
 for _,entry:=range entries{
 if _,err:=strconv.Atoi(entry.Name());err!=nil{continue}
 status,err:=os.ReadFile(filepath.Join("/proc",entry.Name(),"status"));if errors.Is(err,os.ErrNotExist){continue};if err!=nil{return fmt.Errorf("cannot inspect process %s: %w",entry.Name(),err)}
 for _,line:=range strings.Split(string(status),"\n"){if strings.HasPrefix(line,"Uid:"){for _,value:=range strings.Fields(strings.TrimPrefix(line,"Uid:")){if value==account.Uid{return fmt.Errorf("service worker process %s is alive",entry.Name())}}}}
 }
 return nil
}

func productOperatorEmptyCgroup(scope string)error{
 if _,err:=os.Lstat(scope);errors.Is(err,os.ErrNotExist){return nil}else if err!=nil{return err}
 return filepath.WalkDir(scope,func(path string,entry fs.DirEntry,walkErr error)error{
 if walkErr!=nil{return walkErr};if entry.Type()&os.ModeSymlink!=0{return errors.New("cgroup inventory contains a symlink")}
 if entry.Name()!="cgroup.procs"{return nil}
 data,err:=os.ReadFile(path);if err!=nil{return err};if strings.TrimSpace(string(data))!=""{return fmt.Errorf("a worker/command scope is alive: %s",path)};return nil
 })
}

type productOperatorRequest struct{Turn,Reason string;DryRun bool}

// A service-owned audit journal must be opened as its owner. SO_PEERCRED on the
// inherited socket authenticates the root front end without trusting CLI claims.
func delegateProductOperator(turn,reason string,dry bool,out,stderr io.Writer)error{
 account,err:=productAdminLookupUser(productsetup.ServiceUser);if err!=nil{return err}
 uid,uidErr:=strconv.ParseUint(account.Uid,10,32);gid,gidErr:=strconv.ParseUint(account.Gid,10,32)
 if uidErr!=nil || gidErr!=nil || uid==0{return adminRefusal("service account IDs are invalid")}
 sockets,err:=unix.Socketpair(unix.AF_UNIX,unix.SOCK_STREAM|unix.SOCK_CLOEXEC,0);if err!=nil{return err}
 parent,child:=os.NewFile(uintptr(sockets[0]),"root-operator"),os.NewFile(uintptr(sockets[1]),"operator-request")
 defer parent.Close();defer child.Close()
 executable,err:=os.Executable();if err!=nil{return err}
 cmd:=exec.Command(executable,"product","turns","resolve-worker")
 cmd.SysProcAttr=&syscall.SysProcAttr{Credential:&syscall.Credential{Uid:uint32(uid),Gid:uint32(gid)}}
 cmd.ExtraFiles=[]*os.File{child};cmd.Stdout,cmd.Stderr=out,stderr
 if err:=cmd.Start();err!=nil{return err}
 err=json.NewEncoder(parent).Encode(productOperatorRequest{turn,reason,dry})
 parent.Close();waitErr:=cmd.Wait()
 if err!=nil{return err};if waitErr!=nil{return adminRefusal("operator evidence check refused (see diagnostic above): %v",waitErr)};return nil
}

func runProductOperatorWorker(out io.Writer)error{
 socket:=os.NewFile(3,"operator-request");defer socket.Close()
 peer,err:=unix.GetsockoptUcred(3,unix.SOL_SOCKET,unix.SO_PEERCRED)
 if err!=nil || peer.Uid!=0{return adminRefusal("operator request requires an inherited root-authenticated socket")}
 var request productOperatorRequest
 if err:=json.NewDecoder(io.LimitReader(socket,4096)).Decode(&request);err!=nil{return adminRefusal("invalid operator request: %v",err)}
 cfg,err:=loadProductServiceConfig(productsetup.ServiceConfigPath);if err!=nil{return err}
 if err:=cfg.validate(nil);err!=nil{return err}
 registration,err:=productsession.LoadRegistration(cfg.RegistrationPath,cfg.RegistrationSignaturePath,cfg.RegistrationPublicKeyPath);if err!=nil{return err}
 env:=&productTurnEnv{Registration:registration,StateRoot:cfg.StateRoot,ProjectRoots:cfg.Projects,
 StorePolicyDigest:digestBytes([]byte(gate.ProductionSessionPolicyLabel)),MetadataPolicyDigest:digestBytes([]byte(gate.ProductionMetadataPolicyLabel))}
 if err:=installProductGatePolicies(env,cfg.GatePolicyPaths);err!=nil{return err}
 if env.Xattr,err=productHostXattr(cfg,env);err!=nil{return err}
 turn,err:=productsession.OperatorResolve(cfg.CatalogPath,registration,request.Turn,request.Reason,request.DryRun,
 func(turn productsession.Turn)(productsession.OperatorEvidence,error){return env.operatorEvidence(context.Background(),turn)})
 if err!=nil{return adminRefusal("operator resolution refused: %v",err)}
 if request.DryRun{fmt.Fprintf(out,"dry-run: %s can be abandoned; baseline unchanged; full reservation charged; no records written\n",turn.ID)}else{
 fmt.Fprintf(out,"resolved %s: abandoned (operator_resolved); baseline unchanged; full reservation charged; session and project unblocked\n",turn.ID)}
 return nil
}

func (e *productTurnEnv) operatorEvidence(ctx context.Context,turn productsession.Turn)(productsession.OperatorEvidence,error){
 var evidence productsession.OperatorEvidence
 if turn.Link==nil{return evidence,errors.New("turn has no workflow link")}
 paths,err:=productTurnPathsFromLink(*turn.Link);if err!=nil{return evidence,err}
 if filepath.Dir(paths.StoreJournalPath)!=filepath.Join(e.StateRoot,"turns",turn.Link.WorkflowID){return evidence,errors.New("turn journals are outside the registered state root")}
 retained,err:=openRetainedProductTurn(paths,e.storeOptions(turn),nil);if err!=nil{return evidence,fmt.Errorf("retained Store/journals: %w",err)}
 defer retained.Close()
 bundle,err:=retained.Store.EvidenceBundle();if err!=nil{return evidence,err}
 checkpoint,err:=loadRetainedProductCheckpoint(ctx,filepath.Dir(paths.StoreJournalPath),turn.Link.WorkflowID);if err!=nil{return evidence,fmt.Errorf("protected checkpoint: %w",err)}
 // An abandoned provider request cannot execute after its sole supervisor died.
 // Keep every effect check; only provider-side pending state is allowed to be inert.
 pins:=checkpoint.Pins;pins.HostCallReservation=nil;pins.PendingCallDisposition=""
 if err:=productTurnChildResolved(bundle,pins);err!=nil{return evidence,fmt.Errorf("Store/workflow is not inert: %w",err)}
 command,err:=retained.CommandJournal.Trace();if err!=nil{return evidence,err}
 if len(command.Records)!=0{return evidence,errors.New("command activity exists; command-scope recovery is not supported")}
 if named,_,err:=e.workflowPublication(turn.ProjectID,turn.Link.WorkflowID);err!=nil || named{return evidence,errors.Join(errors.New("publication evidence is present or unreadable"),err)}
 measured,err:=e.MeasureProjectBaseline(turn.ProjectID);if err!=nil{return evidence,fmt.Errorf("baseline scan: %w",err)}
 if measured!=turn.BaselineDigest{return evidence,errProductStaleBaseline}
 head:=func(j *audit.Journal)(string,error){trace,err:=j.Trace();if err!=nil{return "",err};if len(trace.Records)==0{return digestBytes(nil),nil};return "sha256:"+auditHeadHash(trace.Records),nil}
 evidence=productsession.OperatorEvidence{WorkflowID:turn.Link.WorkflowID,BaselineDigest:turn.BaselineDigest,ScanDigest:measured,CheckpointDigest:"sha256:"+checkpoint.ComputedCheckpointDigest}
 if evidence.StoreHead,err=head(retained.StoreJournal);err!=nil{return evidence,err}
 if evidence.ProviderHead,err=head(retained.ProviderJournal);err!=nil{return evidence,err}
 evidence.CommandHead=digestBytes(nil)
 return evidence,nil
}
