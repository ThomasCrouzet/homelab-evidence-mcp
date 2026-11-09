//go:build windows

package config

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// checkCredentialFileACL rejects an explicit allow entry for any identity
// other than the current user, LocalSystem, or Administrators. It uses the
// file DACL because Unix permission bits do not describe Windows access.
func checkCredentialFileACL(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errors.New("cannot read the file access control list")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return errors.New("cannot read the file access control list")
	}
	if dacl == nil || dacl.AceCount == 0 {
		return errors.New("file access control list is empty or missing")
	}
	allowed := make([]*windows.SID, 0, 3)
	if token, err := windows.OpenCurrentProcessToken(); err == nil {
		defer func() { _ = token.Close() }()
		if user, err := token.GetTokenUser(); err == nil {
			allowed = append(allowed, user.User.Sid)
		}
	}
	if sid, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid); err == nil {
		allowed = append(allowed, sid)
	}
	if sid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid); err == nil {
		allowed = append(allowed, sid)
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return errors.New("cannot read the file access control list")
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		permitted := false
		for _, candidate := range allowed {
			if candidate != nil && sid.Equals(candidate) {
				permitted = true
				break
			}
		}
		if !permitted {
			return errors.New("token file gives access to an identity other than the owner, SYSTEM, or Administrators")
		}
	}
	return nil
}
