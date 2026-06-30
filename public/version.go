package public

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
)

// Version is a short content hash of all embedded assets, set once at init time.
// ponytail: computed once at startup; changes whenever any file in FS changes.
var Version string

func init() {
	h := sha256.New()
	_ = fs.WalkDir(FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		f, err := FS.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, _ = io.Copy(h, f)
		return nil
	})
	Version = hex.EncodeToString(h.Sum(nil))[:12]
}
