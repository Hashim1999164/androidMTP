package device

import (
	"log"
	"regexp"

	"github.com/hanwen/go-mtpfs/mtp"
)

// SelectStorages returns hierarchical/DCF storage IDs matching an optional regex.
func SelectStorages(dev *mtp.Device, pat string) ([]uint32, error) {
	sids := mtp.Uint32Array{}
	if err := dev.GetStorageIDs(&sids); err != nil {
		return nil, err
	}

	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, err
	}

	var filtered []uint32
	for _, id := range sids.Values {
		var s mtp.StorageInfo
		if err := dev.GetStorageInfo(id, &s); err != nil {
			return nil, err
		}
		if !s.IsHierarchical() && !s.IsDCF() {
			log.Printf("skipping non hierarchical or DCF storage %q", s.StorageDescription)
			continue
		}
		if re.FindStringIndex(s.StorageDescription) == nil {
			continue
		}
		filtered = append(filtered, id)
	}
	return filtered, nil
}
