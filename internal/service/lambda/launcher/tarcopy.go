package launcher

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyCode streams the code tree into /var/task as a tar archive owned by
// root, keeping the tree's normalized modes (containers.md 5.1).
func (l *Launcher) copyCode(ctx context.Context, id, dir string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeTree(pw, dir)) }()
	err := l.client.ContainerArchiveUpload(ctx, id, taskRoot, pr)
	_ = pr.CloseWithError(errors.New("upload ended"))
	return err
}

func writeTree(w io.Writer, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tw := tar.NewWriter(w)
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("code tree entry %s is neither a file nor a directory", name)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(name)
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "root", "root"
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return fmt.Errorf("archive code tree %s: %w", dir, err)
	}
	return tw.Close()
}
