package machine

import (
	"errors"
	"testing"
)

// A statfs failure must degrade OK to false, like every other vitals source —
// otherwise Read() reports a healthy machine with 0 total / 0 used disk.
func TestReadDegradesOKOnDiskError(t *testing.T) {
	orig := readRootDisk
	t.Cleanup(func() { readRootDisk = orig })
	readRootDisk = func() (uint64, uint64, error) {
		return 0, 0, errors.New("statfs failed")
	}

	v := Read()
	if v.OK {
		t.Fatal("Read().OK must be false when readRootDisk fails")
	}
	if v.DiskTotalBytes != 0 || v.DiskUsedBytes != 0 {
		t.Fatalf("disk bytes should stay zero on error, got %+v", v)
	}
	// The other portable fields are still stamped (degrade, don't hide).
	if v.Generated == 0 || v.CPUCount == 0 {
		t.Fatalf("non-disk fields should still be populated: %+v", v)
	}
}
