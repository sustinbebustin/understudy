package kernel

import (
	"context"
	"errors"
	"io"
	"log/slog"
)

// Release closes the streaming bodies of an output nobody will encode: one
// an after-commit fault, the strict check, a handler contract breach or a
// failed encode replaced with an error. A smithy.BodySource that holds a
// resource until the encoder takes it, such as an open blob file,
// implements io.Closer; the encoder closes what it takes, and Release
// closes what nobody took. Bodies that hold nothing are left alone.
func Release(out any) error {
	var errs []error
	for _, b := range scanOutput(out).bodies {
		if c, ok := b.(io.Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// discard releases the output of c that the kernel drops, logging a body
// that would not close: the call's answer stands either way.
func (k *Kernel) discard(ctx context.Context, c *Call, out any) {
	if err := Release(out); err != nil {
		k.log.LogAttrs(ctx, slog.LevelWarn, "a dropped output's body did not close; its descriptor may stay open until garbage collection",
			slog.String("op", c.label), slog.String("request_id", string(c.id)), slog.String("error", err.Error()))
	}
}
