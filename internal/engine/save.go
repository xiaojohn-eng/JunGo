package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

func (e *Engine) runSave(ctx context.Context, t Transfer) error {
	source, err := e.openFile(t.Source, "r")
	if err != nil {
		return permanent(err)
	}
	defer source.Close()
	h := sha256.New()
	size, err := io.CopyBuffer(h, contextReader{ctx, source}, make([]byte, 256<<10))
	if err != nil {
		return err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if t.Hash != "" && (t.Hash != digest || t.Size != size) {
		return permanent(errors.New("收件箱源文件已变化，请重新另存"))
	}
	dest, err := e.openFile(t.Destination, "rw")
	if err != nil {
		return permanent(err)
	}
	defer dest.Close()
	info, err := dest.Stat()
	if err != nil {
		return permanent(err)
	}
	if t.Hash == "" && info.Size() > 0 {
		return permanent(errors.New("目标文件已存在，请选择新文件"))
	}
	if t.Completed > info.Size() {
		t.Completed = info.Size()
	}
	if t.Completed > size {
		return permanent(errors.New("无效的另存断点"))
	}
	if _, err = source.Seek(t.Completed, io.SeekStart); err != nil {
		return permanent(err)
	}
	if _, err = dest.Seek(t.Completed, io.SeekStart); err != nil {
		return permanent(errors.New("目标文件不支持断点续传"))
	}
	if err = e.updateTask(t.ID, func(x *Transfer) { x.Hash = digest; x.Size = size; x.Completed = t.Completed }); err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	offset, durable := t.Completed, t.Completed
	for offset < size {
		if err = ctx.Err(); err != nil {
			return err
		}
		count := int64(len(buf))
		if size-offset < count {
			count = size - offset
		}
		n, err := io.ReadFull(source, buf[:count])
		if err != nil {
			return permanent(err)
		}
		written, err := dest.Write(buf[:n])
		offset += int64(written)
		if err != nil {
			return permanent(err)
		}
		if written != n {
			return permanent(io.ErrShortWrite)
		}
		if offset-durable >= 8<<20 || offset == size {
			if err = dest.Sync(); err != nil {
				return permanent(err)
			}
			if err = e.updateTask(t.ID, func(x *Transfer) { x.Completed = offset }); err != nil {
				return err
			}
			durable = offset
		}
	}
	if err = dest.Truncate(size); err != nil {
		return permanent(err)
	}
	if err = dest.Sync(); err != nil {
		return permanent(err)
	}
	if _, err = dest.Seek(0, io.SeekStart); err != nil {
		return permanent(err)
	}
	h.Reset()
	if _, err = io.CopyBuffer(h, contextReader{ctx, dest}, buf); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return permanent(errors.New("另存文件校验失败"))
	}
	return nil
}
