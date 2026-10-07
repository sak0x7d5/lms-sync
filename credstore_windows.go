//go:build windows

package main

import (
	"errors"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// ---------------------------------------------------------------------------
// Windows Credential Manager, through advapi32
//
// A generic credential, persisted for this user on this machine and
// encrypted with the user's login (DPAPI). It is listed in Control Panel >
// Credential Manager > Windows Credentials as lms-sync:<account>, where a
// student can see it and remove it.
// ---------------------------------------------------------------------------

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
	errorNotFound           = syscall.Errno(1168)
)

var (
	advapi32       = syscall.NewLazyDLL("advapi32.dll")
	procCredWrite  = advapi32.NewProc("CredWriteW")
	procCredRead   = advapi32.NewProc("CredReadW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

// credential is CREDENTIALW. Go lays fields out with the same natural
// alignment as C, so the padding before CredentialBlob falls where the
// Windows headers put it.
type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type credManager struct{}

func platformStore() credStore { return credManager{} }

func (credManager) Name() string { return "Windows Credential Manager" }

func (credManager) NeedsSession() bool { return false }

func credTarget(account string) (*uint16, error) {
	return syscall.UTF16PtrFromString(credService + ":" + account)
}

func (credManager) Get(account string) (string, error) {
	target, err := credTarget(account)
	if err != nil {
		return "", err
	}
	var c *credential
	r, _, callErr := procCredRead.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0,
		uintptr(unsafe.Pointer(&c)))
	if r == 0 {
		if errors.Is(callErr, errorNotFound) {
			return "", errNoCredential
		}
		return "", callErr
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(c)))

	// Stored as UTF-16, the way Credential Manager itself and cmdkey store
	// a password, so the entry reads the same from either side.
	if c.CredentialBlobSize == 0 || c.CredentialBlob == nil {
		return "", nil
	}
	blob := unsafe.Slice(c.CredentialBlob, c.CredentialBlobSize)
	units := make([]uint16, len(blob)/2)
	for i := range units {
		units[i] = uint16(blob[2*i]) | uint16(blob[2*i+1])<<8
	}
	return string(utf16.Decode(units)), nil
}

func (credManager) Set(account, secret string) error {
	target, err := credTarget(account)
	if err != nil {
		return err
	}
	user, err := syscall.UTF16PtrFromString(account)
	if err != nil {
		return err
	}
	units := utf16.Encode([]rune(secret))
	blob := make([]byte, 2*len(units))
	for i, u := range units {
		blob[2*i], blob[2*i+1] = byte(u), byte(u>>8)
	}
	c := credential{
		Type:               credTypeGeneric,
		TargetName:         target,
		CredentialBlobSize: uint32(len(blob)),
		Persist:            credPersistLocalMachine,
		UserName:           user,
	}
	if len(blob) > 0 {
		c.CredentialBlob = &blob[0]
	}
	if r, _, callErr := procCredWrite.Call(uintptr(unsafe.Pointer(&c)), 0); r == 0 {
		return callErr
	}
	return nil
}

func (credManager) Delete(account string) error {
	target, err := credTarget(account)
	if err != nil {
		return err
	}
	if r, _, callErr := procCredDelete.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0); r == 0 {
		if errors.Is(callErr, errorNotFound) {
			return nil
		}
		return callErr
	}
	return nil
}
