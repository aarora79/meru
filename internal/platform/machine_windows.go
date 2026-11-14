// This file reads the processor's name and main memory on Windows, which
// has no /proc files and no sysctl, through the system's own API: the
// registry for the name, and kernel32's GlobalMemoryStatusEx for the
// memory. machine_other.go holds the stand-ins for every other system.

//go:build windows

package platform

import (
	"strings"
	"syscall"
	"unsafe"
)

// windowsCPU returns the processor's name as Windows keeps it in the
// registry, such as "AMD EPYC 7R13 Processor", or "" when it can't read
// it. Every core has a key; the first one's name serves for all.
func windowsCPU() string {
	path, err := syscall.UTF16PtrFromString(`HARDWARE\DESCRIPTION\System\CentralProcessor\0`)
	if err != nil {
		return ""
	}
	var key syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, path, 0, syscall.KEY_READ, &key) != nil {
		return ""
	}
	// defer closes the key when windowsCPU returns.
	defer syscall.RegCloseKey(key) //nolint:errcheck // nothing to do if closing a read-only key fails
	value, err := syscall.UTF16PtrFromString("ProcessorNameString")
	if err != nil {
		return ""
	}
	// The registry hands back UTF-16 text; 256 characters is far more than
	// any processor name. size goes in as the buffer's size in bytes and
	// comes back as the bytes written.
	buf := make([]uint16, 256)
	size := uint32(len(buf) * 2)
	var kind uint32
	// unsafe.Pointer passes the buffer's address to the system call, which
	// is how Go hands memory to a Windows API that writes into it.
	if syscall.RegQueryValueEx(key, value, nil, &kind, (*byte)(unsafe.Pointer(&buf[0])), &size) != nil || kind != syscall.REG_SZ { // #nosec G103 -- a buffer this function owns, for the API to fill
		return ""
	}
	return strings.TrimSpace(syscall.UTF16ToString(buf))
}

// memoryStatusEx is Windows' MEMORYSTATUSEX structure, field for field, as
// GlobalMemoryStatusEx fills it. Only totalPhys, main memory in bytes, is
// read; the rest are there so the layout matches.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// windowsMemoryGB returns main memory in whole gigabytes, rounded down as
// on macOS, or 0 when Windows doesn't say. kernel32.dll is one of Windows'
// known DLLs, which always load from the system folder, so loading it by
// name can't pick up a planted copy.
func windowsMemoryGB() int {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if proc.Find() != nil {
		return 0
	}
	s := memoryStatusEx{}
	// The API checks length to know which version of the structure it got.
	s.length = uint32(unsafe.Sizeof(s))                // #nosec G103 -- the size of a fixed structure
	ok, _, _ := proc.Call(uintptr(unsafe.Pointer(&s))) // #nosec G103 -- a structure this function owns, for the API to fill
	if ok == 0 {
		return 0
	}
	return int(s.totalPhys >> 30) // >> 30 divides by 1024 cubed.
}
