package media

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/star574/obd-dicom/dictionary/transfersyntax"
)

type BufData struct {
	BigEndian bool
	MS        *MemoryStream
}

// NewEmptyBufData -
func NewEmptyBufData() *BufData {
	return &BufData{
		BigEndian: false,
		MS:        NewEmptyMemoryStream(),
	}
}

// NewBufDataFromBytes -
func NewBufDataFromBytes(data []byte) *BufData {
	return &BufData{
		BigEndian: false,
		MS:        NewMemoryStreamFromBytes(data),
	}
}

// NewBufDataFromFile -
func NewBufDataFromFile(fileName string) (*BufData, error) {
	ms, err := NewMemoryStreamFromFile(fileName)
	if err != nil {
		return nil, err
	}
	return &BufData{
		BigEndian: false,
		MS:        ms,
	}, nil
}

func (bd *BufData) ClearMemoryStream() {
	bd.MS.Clear()
}

func (bd *BufData) IsBigEndian() bool {
	return bd.BigEndian
}

func (bd *BufData) SetBigEndian(isBigEndian bool) {
	bd.BigEndian = isBigEndian
}

func (bd *BufData) GetPosition() int {
	return bd.MS.GetPosition()
}

func (bd *BufData) SetPosition(position int) {
	bd.MS.SetPosition(position)
}

func (bd *BufData) GetSize() int {
	return bd.MS.GetSize()
}

func (bd *BufData) Read(count int) ([]byte, error) {
	return bd.MS.Read(count)
}

func (bd *BufData) ReadByte() (byte, error) {
	c, err := bd.MS.Read(1)
	if err != nil {
		return 0, err
	}
	return c[0], nil
}

func (bd *BufData) ReadUint16() (uint16, error) {
	c, err := bd.MS.Read(2)
	if err != nil {
		return 0, err
	}
	if bd.BigEndian {
		return binary.BigEndian.Uint16(c), nil
	}
	return binary.LittleEndian.Uint16(c), nil
}

func (bd *BufData) ReadUint32() (uint32, error) {
	c, err := bd.MS.Read(4)
	if err != nil {
		return 0, err
	}
	if bd.BigEndian {
		return binary.BigEndian.Uint32(c), nil
	}
	return binary.LittleEndian.Uint32(c), nil
}

func (bd *BufData) Write(data []byte, count int) (int, error) {
	return bd.MS.Write(data, count)
}

func (bd *BufData) WriteAETitle(aeTitle string) {
	endPos := bd.GetPosition() + 16
	bd.WriteString(aeTitle)
	for bd.GetPosition() < endPos {
		bd.Write([]byte{0x20}, 1)
	}
}

// WriteByte writes a byte
func (bd *BufData) WriteByte(value byte) error {
	_, err := bd.MS.Write([]byte{value}, 1)
	return err
}

// WriteUint16 writes an unsigned int
func (bd *BufData) WriteUint16(value uint16) {
	c := make([]byte, 2)
	if bd.BigEndian {
		binary.BigEndian.PutUint16(c, value)
	} else {
		binary.LittleEndian.PutUint16(c, value)
	}
	bd.MS.Write(c, 2)
}

// WriteUint32 writes an unsigned int
func (bd *BufData) WriteUint32(value uint32) {
	c := make([]byte, 4)
	if bd.BigEndian {
		binary.BigEndian.PutUint32(c, value)
	} else {
		binary.LittleEndian.PutUint32(c, value)
	}
	bd.MS.Write(c, 4)
}

func (bd *BufData) WriteString(value string) {
	bd.MS.Write([]byte(value), len(value))
}

// ReadTag - read a single tag from the Stream
func (bd *BufData) ReadTag(explicitVR bool, opt ...*ParseOptions) (*DcmTag, error) {
	group, err := bd.ReadUint16()
	if err != nil {
		return nil, err
	}
	if len(opt) > 0 {
		stop := false
		switch group {
		case 0x0002:
		case 0x0008:
			stop = opt[0].OnlyMetaHeader
		case 0x0010:
		case 0x0028:
			stop = opt[0].SkipPixelData
		default:
			stop = opt[0].UntilPatientTag
		}
		if stop {
			return nil, nil
		}
	}
	element, err := bd.ReadUint16()
	if err != nil {
		return nil, err
	}
	tag := &DcmTag{
		Group:   group,
		Element: element,
	}

	internalVR := explicitVR

	if tag.Group == 0x0002 {
		internalVR = true
	}

	if (tag.Group != 0x0000) && (tag.Group != 0xfffe) && (internalVR) {
		tag.VR = bd.readString(2)
		if (tag.VR == "OB") || (tag.VR == "OW") || (tag.VR == "SQ") || (tag.VR == "UN") || (tag.VR == "UT") {
			_, err := bd.ReadUint16()
			if err != nil {
				return nil, err
			}

			length, err := bd.ReadUint32()
			if err != nil {
				return nil, err
			}

			tag.Length = length
		} else {
			length, err := bd.ReadUint16()
			if err != nil {
				return nil, err
			}
			tag.Length = uint32(length)
		}
	} else {
		if !internalVR {
			tag.VR = getDictionaryVR(tag.Group, tag.Element)
		}
		length, err := bd.ReadUint32()
		if err != nil {
			return nil, err
		}
		tag.Length = length
	}

	if (tag.Length != 0) && (tag.Length != 0xFFFFFFFF) {
		if data, err := bd.MS.Read(int(tag.Length)); err == nil {
			tag.Data = data
		} else {
			return nil, err
		}
	}
	if len(opt) == 0 || !opt[0].SkipFillTag {
		FillTag(tag)
	}
	return tag, nil
}

// WriteTag - Write a single tag to stream
func (bd *BufData) WriteTag(tag *DcmTag, explicitVR bool) {
	bd.WriteUint16(tag.Group)
	bd.WriteUint16(tag.Element)
	// If the byte length is not even, append 1 padding byte to make it even.
	// https://dicom.nema.org/medical/dicom/current/output/html/part05.html#sect_8.1.1
	padding := false
	if tag.Length%2 != 0 && tag.Length != 0xFFFFFFFF {
		tag.Length += 1
		padding = true
	}
	if (tag.Group != 0x0000) && (tag.Group != 0xfffe) && (explicitVR) {
		if tag.VR == "" { // In case converting from illicit
			tag.VR = getDictionaryVR(tag.Group, tag.Element)
		}
		bd.MS.Write([]byte(tag.VR), 2)
		if (tag.VR == "OB") || (tag.VR == "OW") || (tag.VR == "SQ") || (tag.VR == "UN") || (tag.VR == "UT") {
			bd.WriteUint16(0)
			bd.WriteUint32(tag.Length)
		} else {
			bd.WriteUint16(uint16(tag.Length))
		}
	} else {
		bd.WriteUint32(tag.Length)
	}
	if (tag.Length != 0) && (tag.Length != 0xFFFFFFFF) {
		if padding {
			tag.Data = append(tag.Data, 0)
			padding = false
		}
		bd.MS.Write(tag.Data, int(tag.Length))
	}
}

// WriteStringTag - Writes a String to a DICOM tag
func (bd *BufData) WriteStringTag(group uint16, element uint16, vr string, content string, explicitVR bool) {
	data := []byte(content)
	length := uint32(len(data))
	if length%2 == 1 {
		length++
		if vr == "UI" {
			data = append(data, 0x00)
		} else {
			data = append(data, 0x20)
		}
	}
	tag := &DcmTag{
		Group:     group,
		Element:   element,
		Length:    length,
		VR:        vr,
		Data:      data,
		BigEndian: false,
	}
	bd.WriteTag(tag, explicitVR)
}

// ReadMeta - Read Meta Header
func (bd *BufData) ReadMeta() (*transfersyntax.TransferSyntax, error) {
	var TransferSyntax *transfersyntax.TransferSyntax
	pos := 0

	bd.SetPosition(128)
	bs, err := bd.MS.Read(4)
	if err != nil {
		return nil, err
	}
	if string(bs[:4]) == "DICM" {
		fin := false
		for (pos < bd.GetSize()) && (!fin) {
			pos = bd.GetPosition()
			tag, _ := bd.ReadTag(true)
			if (tag.Group == 0x02) && (tag.Element == 0x010) {
				uid := tag.getString()
				TransferSyntax = transfersyntax.GetTransferSyntaxFromUID(uid)
			}
			if tag.Group > 0x02 {
				fin = true
			}
		}
	}
	bd.SetPosition(pos)
	return TransferSyntax, nil
}

// WriteMeta - Write Meta Header
func (bd *BufData) WriteMeta(SOPClassUID string, SOPInstanceUID string, TransferSyntax string) {
	explicitVR := true
	buffer := make([]byte, 128)
	var largo uint32
	var tag *DcmTag

	bd.MS.Write(buffer, 128)
	bd.MS.Write([]byte("DICM"), 4)
	tag = &DcmTag{
		Group:     0x02,
		Element:   0x00,
		Length:    4,
		VR:        "UL",
		Data:      []byte{0, 0, 0, 0},
		BigEndian: bd.IsBigEndian()}
	bd.WriteTag(tag, explicitVR)
	tag = &DcmTag{
		Group:     0x02,
		Element:   0x01,
		Length:    2,
		VR:        "OB",
		Data:      []byte{0x00, 0x01},
		BigEndian: bd.IsBigEndian(),
	}
	bd.WriteTag(tag, explicitVR)

	bd.WriteStringTag(0x02, 0x02, "UI", SOPClassUID, explicitVR)
	bd.WriteStringTag(0x02, 0x03, "UI", SOPInstanceUID, explicitVR)
	bd.WriteStringTag(0x02, 0x10, "UI", TransferSyntax, explicitVR)

	// Implementation Class UID
	bd.WriteStringTag(0x02, 0x12, "UI", "123456", explicitVR)
	// Implementation Version Name
	bd.WriteStringTag(0x02, 0x13, "SH", "odb", explicitVR)

	// calculate group length and go Back to group size tag
	ptr := bd.GetPosition()
	largo = uint32(bd.GetSize() - 12 - 128 - 4)
	bd.SetPosition(128 + 4 + 8)
	bd.WriteUint32(largo)
	bd.SetPosition(ptr)
}

// ReadObj - Read a DICOM Object from a BufData
func (bd *BufData) ReadObj(obj *DcmObj, opt ...*ParseOptions) error {
	isExplicitVR := obj.IsExplicitVR()
	obj.Size = bd.GetSize()
	for bd.GetPosition() < bd.GetSize() {
		tag, err := bd.ReadTag(isExplicitVR, opt...)
		if err != nil {
			return err
		}
		if tag == nil {
			return nil
		}
		if !isExplicitVR {
			tag.VR = getDictionaryVR(tag.Group, tag.Element)
		}
		if tag.Length%2 != 0 && tag.VR != "SQ" && tag.Length != 0xffffffff {
			return fmt.Errorf("%s is odd", tag.Name)
		}
		obj.Add(tag)
	}
	return nil
}

// WriteObj - Write a DICOM Object to a BufData
func (bd *BufData) WriteObj(obj *DcmObj) {
	//	bd.BigEndian = BigEndian
	// Si lo limpio elimino el meta!!
	//	bd.MS.Clear()
	for i := 0; i < obj.TagCount(); i++ {
		tag := obj.GetTagAt(i)
		bd.WriteTag(tag, obj.IsExplicitVR())
	}
}

func (bd *BufData) Send(rw *bufio.ReadWriter) error {
	bd.SetPosition(0)
	buffer, _ := bd.MS.Read(bd.GetSize())
	bd.MS.Clear()

	_, err := rw.Write(buffer)
	if err != nil {
		return errors.New("ERROR, bufdata::Send, " + err.Error())
	}
	rw.Flush()
	return nil
}

func (bd *BufData) GetAllBytes() []byte {
	return bd.MS.GetData()
}

func (bd *BufData) readString(length int) string {
	temp, _ := bd.MS.Read(length)
	return string(temp)
}
