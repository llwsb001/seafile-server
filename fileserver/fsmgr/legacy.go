package fsmgr

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

const detectObjectVersion = -1

var errLegacyObjectWrite = errors.New("legacy FS object creation requires the C fileserver")

// The fileserver reads through store IDs that may be shared by virtual repos.
// The old binary objects have a type prefix distinct from a zlib stream.
func defaultReadVersion() int {
	if option.DBType == "sqlite" {
		return detectObjectVersion
	}
	return 1
}

func isLegacyObject(version int, data []byte, objectType uint32) bool {
	return version == 0 || (version == detectObjectVersion && len(data) >= 4 &&
		binary.BigEndian.Uint32(data) == objectType)
}

// Legacy objects use the binary layouts in common/fs-mgr.c.
func decodeLegacyFile(id string, data []byte) (*Seafile, error) {
	const headerSize = 12 // 32-bit type, 64-bit file size
	if len(data) < headerSize || binary.BigEndian.Uint32(data) != SeafMetadataTypeFile ||
		(len(data)-headerSize)%sha1.Size != 0 {
		return nil, fmt.Errorf("invalid legacy file object %s", id)
	}
	file := &Seafile{
		FileID:   id,
		FileType: SeafMetadataTypeFile,
		FileSize: binary.BigEndian.Uint64(data[4:headerSize]),
		BlkIDs:   make([]string, 0, (len(data)-headerSize)/sha1.Size),
	}
	for data = data[headerSize:]; len(data) > 0; data = data[sha1.Size:] {
		file.BlkIDs = append(file.BlkIDs, hex.EncodeToString(data[:sha1.Size]))
	}
	return file, nil
}

func decodeLegacyDir(id string, data []byte) (*SeafDir, error) {
	const typeSize = 4
	const entryHeaderSize = 48 // 32-bit mode, 40-byte hex ID, 32-bit name length
	if len(data) < typeSize || binary.BigEndian.Uint32(data) != SeafMetadataTypeDir {
		return nil, fmt.Errorf("invalid legacy directory object %s", id)
	}
	dir := &SeafDir{DirID: id, DirType: SeafMetadataTypeDir}
	data = data[typeSize:]
	for len(data) > 0 {
		if len(data) < entryHeaderSize {
			return nil, fmt.Errorf("legacy directory %s has an incomplete entry", id)
		}
		nameLen := binary.BigEndian.Uint32(data[44:entryHeaderSize])
		entryID := string(data[4:44])
		if nameLen == 0 || uint64(nameLen) > uint64(len(data)-entryHeaderSize) || !utils.IsObjectIDValid(entryID) {
			return nil, fmt.Errorf("legacy directory %s has an invalid entry", id)
		}
		entrySize := entryHeaderSize + int(nameLen)
		dir.Entries = append(dir.Entries, &SeafDirent{
			Mode: binary.BigEndian.Uint32(data),
			ID:   entryID,
			Name: string(data[entryHeaderSize:entrySize]),
		})
		data = data[entrySize:]
	}
	return dir, nil
}
