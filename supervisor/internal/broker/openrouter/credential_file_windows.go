//go:build windows

package openrouter

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

const (
	seFileObject             = 1
	ownerSecurityInformation = 0x1
	daclSecurityInformation  = 0x4
	aclSizeInformation       = 2
	accessAllowedACEType     = 0
	accessDeniedACEType      = 1
	fileFlagSequentialScan   = 0x08000000
)

var (
	advapi32ForCredential = syscall.NewLazyDLL("advapi32.dll")
	procGetSecurityInfo   = advapi32ForCredential.NewProc("GetSecurityInfo")
	procGetACLInformation = advapi32ForCredential.NewProc("GetAclInformation")
	procGetACE            = advapi32ForCredential.NewProc("GetAce")
)

type credentialACLSizeInformation struct {
	AceCount      uint32
	AclBytesInUse uint32
	AclBytesFree  uint32
}

type credentialACEHeader struct {
	AceType  byte
	AceFlags byte
	AceSize  uint16
}

func readSecureCredentialFile(path string) ([]byte, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		name,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT|fileFlagSequentialScan,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), "openrouter-credential")
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, os.ErrInvalid
	}
	defer file.Close()

	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil ||
		info.FileAttributes&(syscall.FILE_ATTRIBUTE_DIRECTORY|syscall.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return nil, os.ErrPermission
	}
	fileInfo, err := file.Stat()
	if err != nil || !fileInfo.Mode().IsRegular() {
		return nil, os.ErrPermission
	}
	if err := verifyCredentialFileOwnerAndDACL(handle); err != nil {
		return nil, os.ErrPermission
	}
	content, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(content) > maxCredentialBytes {
		return nil, os.ErrInvalid
	}
	return content, nil
}

func verifyCredentialFileOwnerAndDACL(handle syscall.Handle) error {
	var owner *syscall.SID
	var dacl uintptr
	var descriptor uintptr
	result, _, _ := procGetSecurityInfo.Call(
		uintptr(handle), seFileObject, ownerSecurityInformation|daclSecurityInformation,
		uintptr(unsafe.Pointer(&owner)), 0, uintptr(unsafe.Pointer(&dacl)), 0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if result != 0 {
		return syscall.Errno(result)
	}
	defer syscall.LocalFree(syscall.Handle(descriptor))
	if owner == nil || dacl == 0 {
		return os.ErrPermission
	}

	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return os.ErrPermission
	}
	ownerSID, err := owner.String()
	if err != nil {
		return err
	}
	currentSID, err := user.User.Sid.String()
	if err != nil || ownerSID != currentSID {
		return os.ErrPermission
	}

	var aclInfo credentialACLSizeInformation
	result, _, _ = procGetACLInformation.Call(
		dacl, uintptr(unsafe.Pointer(&aclInfo)), unsafe.Sizeof(aclInfo), aclSizeInformation,
	)
	if result == 0 {
		return syscall.GetLastError()
	}
	for index := uint32(0); index < aclInfo.AceCount; index++ {
		var ace uintptr
		result, _, _ = procGetACE.Call(dacl, uintptr(index), uintptr(unsafe.Pointer(&ace)))
		if result == 0 || ace == 0 {
			return os.ErrPermission
		}
		header := (*credentialACEHeader)(unsafe.Pointer(ace))
		switch header.AceType {
		case accessDeniedACEType:
			continue
		case accessAllowedACEType:
			const sidOffset = unsafe.Sizeof(credentialACEHeader{}) + unsafe.Sizeof(uint32(0))
			if uintptr(header.AceSize) < sidOffset+8 {
				return os.ErrPermission
			}
			mask := *(*uint32)(unsafe.Pointer(ace + unsafe.Sizeof(credentialACEHeader{})))
			const (
				genericAll     = 0x10000000
				genericExecute = 0x20000000
				fileExecute    = 0x00000020
			)
			if mask&(genericAll|genericExecute|fileExecute) != 0 {
				return os.ErrPermission
			}
			aceSID := (*syscall.SID)(unsafe.Pointer(ace + sidOffset))
			aceSIDString, err := aceSID.String()
			if err != nil || aceSIDString != currentSID {
				return os.ErrPermission
			}
		default:
			// Unknown/callback ACE forms can carry conditional grants; fail closed.
			return os.ErrPermission
		}
	}
	return nil
}
