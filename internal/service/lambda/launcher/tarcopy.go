package launcher

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// copyCode streams the code tree into /var/task as a tar archive owned by
// root, keeping the tree's normalized modes (containers.md 5.1).
func (l *Launcher) copyCode(ctx context.Context, id, dir string) error {
	return l.upload(ctx, id, taskRoot, dir, "")
}

// copyOpt streams the merged layer tree to / as one archive whose entries
// are an opt directory, with the tree's own mode, then the tree under it,
// owned by root with the tree's modes, so the image need not have /opt
// (lambda-destinations-layers.md 11.5).
func (l *Launcher) copyOpt(ctx context.Context, id, dir string) error {
	return l.upload(ctx, id, "/", dir, path.Base(optRoot))
}

// upload streams dir as a tar archive to at in the container, its entries
// under prefix when prefix is not empty (prefix itself first, as dir's own
// entry), else at the archive's root.
func (l *Launcher) upload(ctx context.Context, id, at, dir, prefix string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeTree(pw, dir, prefix)) }()
	err := l.client.ContainerArchiveUpload(ctx, id, at, pr)
	_ = pr.CloseWithError(errors.New("upload ended"))
	return err
}

func writeTree(w io.Writer, dir, prefix string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tw := tar.NewWriter(w)
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || name == "." && prefix == "" {
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
		hdr.Name = path.Join(prefix, filepath.ToSlash(name))
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
