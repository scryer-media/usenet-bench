package adversarial

import (
	"os"
	"syscall"
)

func hasMultipleLinks(f *os.File) (bool, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info); err != nil {
		return false, err
	}
	return info.NumberOfLinks > 1, nil
}
