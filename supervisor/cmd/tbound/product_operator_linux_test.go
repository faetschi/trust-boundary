//go:build linux

package main

import (
 "bytes"
 "context"
 "errors"
 "io"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "tbound/supervisor/internal/productsession"
)

func TestProductOperatorResolveFrontEnd(t *testing.T){
 oldUID,oldStop,oldDelegate:=productOperatorEUID,productOperatorStopped,productOperatorDelegate
 t.Cleanup(func(){productOperatorEUID,productOperatorStopped,productOperatorDelegate=oldUID,oldStop,oldDelegate})
 for _,mode:=range []string{"root","non-root","running","worker","reason"}{t.Run(mode,func(t *testing.T){
 calls:=0; productOperatorEUID=func()int{return 0};productOperatorStopped=func()error{return nil}
 productOperatorDelegate=func(id,reason string,dry bool,out,stderr io.Writer)error{calls++;return nil}
 // Use the public parser: failures must happen before catalog access/delegation.
 if mode=="non-root"{productOperatorEUID=func()int{return 1000}}
 if mode=="running" || mode=="worker"{productOperatorStopped=func()error{return errors.New("service or worker is alive")}}
 args:=[]string{"turns","resolve","--turn","psess-id/turn-1","--reason","abandon inert work","--dry-run"}
 if mode=="reason" {args=[]string{"turns","resolve","--turn","psess-id/turn-1"}}
 var out,stderr bytes.Buffer
 err:=runProductAdmin(args,&out,&stderr)
 if mode!="root" && (err==nil || calls!=0){t.Fatal("unsafe command accepted")}; if mode=="root" && (err!=nil || calls!=1){t.Fatalf("root: %v, calls=%d",err,calls)}
 })}
}

func TestProductOperatorEvidenceAndAdmission(t *testing.T){
 for _,mode:=range []string{"success","changed","held-journal","dry-run","missing-checkpoint"}{t.Run(mode,func(t *testing.T){
 f,id:=crashProductSoftRefusal(t,"after-capture-refusal-record")
 if err:=f.env.ReconcileAfterRestart(context.Background());err==nil{t.Fatal("expected unknown")}
 turn,_:=f.env.Catalog.Turn(id);if turn.State!=productsession.StateUnknown{t.Fatalf("turn: %+v",turn)}
 path:=filepath.Join(f.env.StateRoot,"catalog.jsonl")
 if mode=="changed"{writeProductDriverFile(t,f.projectDir,"task.txt","external change")}
 if mode=="missing-checkpoint"{if err:=os.Rename(filepath.Join(filepath.Dir(turn.Link.StoreJournalPath),"owner-store","workflow.sqlite"),filepath.Join(f.root,"saved.sqlite"));err!=nil{t.Fatal(err)}}
 var held interface{Close()error}
 if mode=="held-journal"{j,err:=openExistingRecoveryJournal(turn.Link.ProviderJournalPath);if err!=nil{t.Fatal(err)};held=j;defer held.Close()}
 f.env.Catalog.Close();before,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
 _,err=productsession.OperatorResolve(path,f.env.Registration,id,"provider capture failed",mode=="dry-run",func(turn productsession.Turn)(productsession.OperatorEvidence,error){return f.env.operatorEvidence(context.Background(),turn)})
 if mode=="changed" || mode=="held-journal" || mode=="missing-checkpoint"{if err==nil{t.Fatal("unsafe evidence accepted")};t.Log(err);return}
 if err!=nil{t.Fatal(err)}
 if mode=="dry-run"{after,_:=os.ReadFile(path);if string(before)!=string(after){t.Fatal("dry run wrote")};return}
 reopened:=openProductDriverFixture(t,f.root);reopened.session=f.session
 next:=reopened.submit(t,"after-resolution","continue")
 if next.Turn.PredecessorTurnID!=""{t.Fatal("unknown context imported")}
 if _,_,err:=reopened.env.Catalog.CreateSession(f.principal,productsession.CreateSessionRequest{ProjectID:productDriverProject,ProviderProfile:productDriverProfile,PublicationMode:"auto",Budget:f.session.Budget,RequestKey:"new-session"},turn.BaselineDigest);err!=nil && !strings.Contains(err.Error(),"PROJECT_BUSY"){t.Fatal(err)}
 })}
}
