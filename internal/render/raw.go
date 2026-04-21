package render

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/crmarques/gitups/api/v1alpha1"
)

// renderRaw copies every regular file under entry.Dir/raw/ into pkgDir. Files
// ending in .tmpl are rendered through Go text/template with tctx first and
// their suffix stripped.
func renderRaw(ctx context.Context, rp *v1.ResolvedPackage, unit renderUnit, pkgDir string, tctx templateCtx) error {
	_ = rp
	_ = ctx
	src := filepath.Join(unit.SourceDir, "raw")
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			// renderer=raw with no raw/ dir is valid for packages that emit
			// manifests exclusively via overlays (env-specific config packages,
			// stubs). Overlays run after this function returns.
			return nil
		}
		return fmt.Errorf("renderer=raw requires %s: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	return filepath.Walk(src, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstName := strings.TrimSuffix(rel, ".tmpl")
		dst := filepath.Join(pkgDir, dstName)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if strings.HasSuffix(path, ".tmpl") {
			body, err := renderTemplateFile(path, tctx)
			if err != nil {
				return err
			}
			return os.WriteFile(dst, []byte(body), 0o644)
		}
		return copyFile(path, dst)
	})
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	_, err = io.Copy(out, in)
	return err
}
