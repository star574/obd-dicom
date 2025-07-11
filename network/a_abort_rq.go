package network

import (
	"bufio"

	"github.com/star574/obd-dicom/media"
)

type aabortRQ struct {
	ItemType  byte // 0x07
	Reserved1 byte
	Length    uint32
	Reserved2 byte
	Reserved3 byte
	Source    byte
	Reason    byte
}

// NewAAbortRQ - NewAAbortRQ
func NewAAbortRQ() *aabortRQ {
	return &aabortRQ{
		ItemType:  0x07,
		Reserved1: 0x00,
		Reserved2: 0x00,
		Reserved3: 0x01,
		Source:    0x03,
		Reason:    0x01,
	}
}

func (aarq *aabortRQ) GetReason() string {
	return PermanentRejectReasons[aarq.Reason]
}

func (aarq *aabortRQ) Size() uint32 {
	aarq.Length = 4
	return aarq.Length + 6
}

func (aarq *aabortRQ) Write(rw *bufio.ReadWriter) error {
	bd := media.NewEmptyBufData()

	bd.SetBigEndian(true)
	aarq.Size()
	bd.WriteByte(aarq.ItemType)
	bd.WriteByte(aarq.Reserved1)
	bd.WriteUint32(aarq.Length)
	bd.WriteByte(aarq.Reserved2)
	bd.WriteByte(aarq.Reserved3)
	bd.WriteByte(aarq.Source)
	bd.WriteByte(aarq.Reason)

	return bd.Send(rw)
}

func (aarq *aabortRQ) Read(ms *media.MemoryStream) (err error) {
	if aarq.ItemType, err = ms.GetByte(); err != nil {
		return err
	}
	return aarq.ReadDynamic(ms)
}

func (aarq *aabortRQ) ReadDynamic(ms *media.MemoryStream) (err error) {
	if aarq.Reserved1, err = ms.GetByte(); err != nil {
		return err
	}
	if aarq.Length, err = ms.GetUint32(); err != nil {
		return err
	}
	if aarq.Reserved2, err = ms.GetByte(); err != nil {
		return err
	}
	if aarq.Reserved3, err = ms.GetByte(); err != nil {
		return err
	}
	if aarq.Source, err = ms.GetByte(); err != nil {
		return err
	}
	if aarq.Reason, err = ms.GetByte(); err != nil {
		return err
	}
	return
}
