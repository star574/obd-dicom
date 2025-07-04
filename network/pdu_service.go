package network

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/sirupsen/logrus"
	"net"
	"time"

	"github.com/star574/obd-dicom/dictionary/sopclass"
	"github.com/star574/obd-dicom/dictionary/tags"
	"github.com/star574/obd-dicom/dictionary/transfersyntax"
	"github.com/star574/obd-dicom/imp"
	"github.com/star574/obd-dicom/media"
	"github.com/star574/obd-dicom/network/dicomcommand"
	"github.com/star574/obd-dicom/network/dicomstatus"
	"github.com/star574/obd-dicom/network/pdutype"
	"github.com/star574/obd-dicom/network/priority"
)

type pduService struct {
	AcceptedPresentationContexts []*presentationContextAccept
	readWriter                   *bufio.ReadWriter
	ms                           *media.MemoryStream
	pdutype                      int
	pdulength                    uint32
	AssocRQ                      *AAssociationRQ
	AssocAC                      *aassociationAC
	AssocRJ                      *aassociationRJ
	ReleaseRQ                    *areleaseRQ
	ReleaseRP                    *areleaseRP
	AbortRQ                      *aabortRQ
	Pdata                        PDataTF
	Timeout                      int
	OnAssociationRequest         func(request *AAssociationRQ) bool
	OnAssociationRelease         func(request *AAssociationRQ)
	Conn                         net.Conn
	commandField                 uint16
}

// newPDUService - creates a pointer to PDUService
func newPDUService() *pduService {
	return &pduService{
		ms:        media.NewEmptyMemoryStream(),
		AssocRQ:   NewAAssociationRQ(),
		AssocAC:   NewAAssociationAC(),
		AssocRJ:   NewAAssociationRJ(),
		ReleaseRQ: NewAReleaseRQ(),
		ReleaseRP: NewAReleaseRP(),
		AbortRQ:   NewAAbortRQ(),
	}
}

var maxPduLength uint32 = 16384

func (pdu *pduService) SetConn(rw *bufio.ReadWriter) {
	pdu.readWriter = rw
}

func (pdu *pduService) GetTransferSyntax(pcid byte) *transfersyntax.TransferSyntax {
	for _, pca := range pdu.AcceptedPresentationContexts {
		if pca.GetPresentationContextID() == pcid {
			return transfersyntax.GetTransferSyntaxFromUID(pca.GetTrnSyntax().GetUID())
		}
	}
	return nil
}

func (pdu *pduService) SetTimeout(timeout int) {
	pdu.Timeout = timeout
}

func (pdu *pduService) Connect(IP string, Port string) error {
	conn, err := net.Dial("tcp", IP+":"+Port)
	if err != nil {
		return errors.New("pduservice::Connect - " + err.Error())
	}
	pdu.Conn = conn
	if pdu.Timeout > 0 {
		conn.SetDeadline(time.Now().Add(time.Duration(int32(pdu.Timeout)) * time.Second))
	}

	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

	pdu.readWriter = rw
	pdu.AssocRQ.SetMaxSubLength(maxPduLength)
	pdu.AssocRQ.SetImpClassUID(imp.GetImpClassUID())
	pdu.AssocRQ.SetImpVersionName(imp.GetImpVersion())

	if err = pdu.AssocRQ.Write(pdu.readWriter); err != nil {
		return err
	}

	pdu.ms = media.NewEmptyMemoryStream()

	if err := pdu.ms.ReadFully(rw, 10); err != nil {
		pdu.Close()
		return err
	}

	ItemType, err := pdu.ms.GetByte()
	if err != nil {
		return err
	}

	if _, err = pdu.ms.GetByte(); err != nil {
		return err
	}

	if pdu.pdulength, err = pdu.ms.GetUint32(); err != nil {
		return err
	}

	switch ItemType {
	case pdutype.AssociationAccept:
		if err := pdu.readPDU(); err != nil {
			return err
		}
		pdu.ms.SetPosition(1)
		pdu.AssocAC.ReadDynamic(pdu.ms)
		if !pdu.interogateAAssociateAC() {
			return errors.New("pduservice::Connect - No accepted presentation contexts found")
		}
		return nil
	case pdutype.AssociationReject:
		if err := pdu.readPDU(); err != nil {
			return err
		}
		pdu.ms.SetPosition(1)
		pdu.AssocRJ.ReadDynamic(pdu.ms)
		return fmt.Errorf("pduservice::Connect - Association rejected - %s", pdu.AssocRJ.GetReason())
	case pdutype.AssociationAbortRequest:
		if err := pdu.readPDU(); err != nil {
			return err
		}
		pdu.ms.SetPosition(1)
		pdu.AbortRQ.ReadDynamic(pdu.ms)
		return fmt.Errorf("pduservice::Connect - Association aborted - %s", pdu.AbortRQ.GetReason())
	default:
		return fmt.Errorf("pduservice::Connect - Corrupt transmision - %b", ItemType)
	}
}

func (pdu *pduService) Close() {
	pdu.ReleaseRQ.Write(pdu.readWriter)
	pdu.ReleaseRP.Read(pdu.ms)
	pdu.Conn.Close()
}

func (pdu *pduService) NextPDU() (command *media.DcmObj, err error) {
	if pdu.Pdata.Buffer != nil {
		pdu.Pdata.Buffer.ClearMemoryStream()
	} else {
		pdu.Pdata.Buffer = media.NewEmptyBufData()
	}

	pdu.Pdata.MsgStatus = 0
	if pdu.Pdata.Length != 0 {
		DCO := media.NewEmptyDCMObj()
		pdu.Pdata.ReadDynamic(pdu.ms)
		if pdu.Pdata.MsgStatus > 0 {
			if !pdu.parseRawVRIntoDCM(DCO) {
				pdu.AbortRQ.Write(pdu.readWriter)
				return nil, errors.New("pduservice::Read - ParseRawVRIntoDCM failed")
			}
			return DCO, nil
		}
	}

	for {
		pdu.ms = media.NewEmptyMemoryStream()

		if err := pdu.ms.ReadFully(pdu.readWriter, 10); err != nil {
			return nil, err
		}

		pdu.ms.SetPosition(0)

		if pdu.pdutype, err = pdu.ms.Get(); err != nil {
			return nil, err
		}

		if _, err = pdu.ms.Get(); err != nil {
			return nil, err
		}

		if pdu.pdulength, err = pdu.ms.GetUint32(); err != nil {
			return nil, err
		}

		switch pdu.pdutype {
		case pdutype.AssocicationRequest:
			if err := pdu.readPDU(); err != nil {
				return nil, err
			}
			if err := pdu.AssocRQ.Read(pdu.ms); err != nil {
				return nil, err
			}
			if err := pdu.interogateAAssociateRQ(pdu.readWriter); err != nil {
				return nil, err
			}
			return nil, nil
		case pdutype.AssociationAccept:
			if err := pdu.readPDU(); err != nil {
				return nil, err
			}
			return nil, nil
		case pdutype.PDUDataTransfer:
			if err := pdu.readPDU(); err != nil {
				return nil, err
			}
			pdu.ms.SetPosition(1)
			if err := pdu.Pdata.ReadDynamic(pdu.ms); err != nil {
				return nil, err
			}
			if pdu.Pdata.MsgStatus > 0 {
				DCO := media.NewEmptyDCMObj()
				if !pdu.parseRawVRIntoDCM(DCO) {
					pdu.AbortRQ.Write(pdu.readWriter)
					return nil, errors.New("pduservice::Read - ParseRawVRIntoDCM failed")
				}
				return DCO, nil
			}
		case pdutype.AssociationReleaseRequest:
			logrus.Info("ASSOC-R-RQ:", "CallingAE", pdu.AssocRQ.GetCallingAE(), "CalledAE", pdu.AssocRQ.GetCalledAE())
			if pdu.OnAssociationRelease != nil {
				pdu.OnAssociationRelease(pdu.AssocRQ)
			}
			pdu.ReleaseRQ.ReadDynamic(pdu.ms)
			pdu.ReleaseRP.Write(pdu.readWriter)
			return nil, errors.New("pduservice::Read - A-Release-RQ")
		case pdutype.AssociationReleaseResponse:
			logrus.Info("ASSOC-R-RP:", "CallingAE", pdu.AssocRQ.GetCallingAE(), "CalledAE", pdu.AssocRQ.GetCalledAE())
			return nil, errors.New("pduservice::Read - A-Release-RP")
		case pdutype.AssociationAbortRequest:
			logrus.Info("ASSOC-ABORT-RQ:", "CallingAE", pdu.AssocRQ.GetCallingAE(), "CalledAE", pdu.AssocRQ.GetCalledAE())
			return nil, errors.New("pduservice::Read - A-Abort-RQ")
		default:
			pdu.AbortRQ.Write(pdu.readWriter)
			return nil, errors.New("pduservice::Read - unknown ItemType")
		}
	}
}

func (pdu *pduService) GetAAssociationRQ() *AAssociationRQ {
	return pdu.AssocRQ
}

func (pdu *pduService) GetCalledAE() string {
	return pdu.AssocRQ.GetCalledAE()
}

func (pdu *pduService) GetCallingAE() string {
	return pdu.AssocRQ.GetCallingAE()
}

func (pdu *pduService) SetCalledAE(calledAE string) {
	pdu.AssocRQ.SetCalledAE(calledAE)
}

func (pdu *pduService) SetCallingAE(callingAE string) {
	pdu.AssocRQ.SetCallingAE(callingAE)
}

func (pdu *pduService) AddPresContexts(presentationContext *presentationContext) {
	pdu.AssocRQ.AddPresContexts(presentationContext)
}

func (pdu *pduService) GetPresentationContextID() byte {
	return pdu.Pdata.PresentationContextID
}

func (pdu *pduService) SetOnAssociationRequest(f func(request *AAssociationRQ) bool) {
	pdu.OnAssociationRequest = f
}

func (pdu *pduService) SetOnAssociationRelease(f func(request *AAssociationRQ)) {
	pdu.OnAssociationRelease = f
}

func (pdu *pduService) Write(DCO *media.DcmObj, ItemType byte) error {
	if pdu.Pdata.Buffer != nil {
		pdu.Pdata.Buffer.ClearMemoryStream()
	} else {
		pdu.Pdata.Buffer = media.NewEmptyBufData()
	}

	if pdu.Pdata.PresentationContextID == 0 {
		return errors.New("pduservice::Write - PresentationContextID==0")
	}

	if !pdu.parseDCMIntoRaw(DCO) {
		return errors.New("pduservice::Write - ParseDCMIntoRaw failed")
	}

	pdu.Pdata.MsgHeader = ItemType
	if pdu.AssocAC.GetUserInformation().GetMaxSubLength().GetMaximumLength() > maxPduLength {
		pdu.AssocAC.SetMaxSubLength(maxPduLength)
	}

	// Fixed MaxLength - 6 20200811
	pdu.Pdata.BlockSize = pdu.AssocAC.GetMaxSubLength() - 6

	if ItemType > 0x00 {
		sopClass := sopclass.GetSOPClassFromUID(DCO.GetString(tags.AffectedSOPClassUID))
		logrus.Info("PDU-Service: SOP Class", "UID", sopClass.UID, "Description", sopClass.Description, "CalledAE", pdu.GetCalledAE())
	}

	return pdu.Pdata.Write(pdu.readWriter)
}

func (pdu *pduService) interogateAAssociateAC() bool {
	var PresentationContextID byte
	TS := ""

	for _, presContextAccept := range pdu.AssocAC.GetPresContextAccepts() {
		if presContextAccept.GetResult() == 0 {
			pdu.AcceptedPresentationContexts = append(pdu.AcceptedPresentationContexts, presContextAccept)
			if len(TS) == 0 {
				TS = presContextAccept.GetTrnSyntax().GetUID()
				PresentationContextID = presContextAccept.GetPresentationContextID()
			}
		}
	}
	if (len(TS) > 0) && (len(pdu.AcceptedPresentationContexts) > 0) {
		pdu.Pdata.PresentationContextID = PresentationContextID
		return true
	}
	return false
}

func (pdu *pduService) interogateAAssociateRQ(rw *bufio.ReadWriter) error {
	if pdu.OnAssociationRequest == nil || !pdu.OnAssociationRequest(pdu.AssocRQ) {
		pdu.AssocRJ.Set(1, 7)
		return pdu.AssocRJ.Write(rw)
	}

	pdu.AssocAC.SetCalledAE(pdu.AssocRQ.GetCalledAE())
	pdu.AssocAC.SetCallingAE(pdu.AssocRQ.GetCallingAE())
	pdu.AssocAC.SetAppContext(pdu.AssocRQ.GetAppContext())
	pdu.AssocAC.SetUserInformation(pdu.AssocRQ.GetUserInformation())

	logrus.Info("ASSOC-RQ:", "CallingAE", pdu.AssocRQ.GetCallingAE(), "CalledAE", pdu.AssocRQ.GetCalledAE())
	logrus.Info("ASSOC-RQ:", "ImpClass", pdu.AssocRQ.GetUserInformation().GetImpClass().GetUID())
	logrus.Info("ASSOC-RQ:", "ImpVersion", pdu.AssocRQ.GetUserInformation().GetImpVersion().GetUID())
	logrus.Info("ASSOC-RQ:", "MaxPDULength", pdu.AssocRQ.GetUserInformation().GetMaxSubLength().GetMaximumLength())
	logrus.Info("ASSOC-RQ:", "MaxOpsInvoked", pdu.AssocRQ.GetUserInformation().GetAsyncOperationWindow().GetMaxNumberOperationsInvoked(), "MaxOpsPerformed", pdu.AssocRQ.GetUserInformation().GetAsyncOperationWindow().GetMaxNumberOperationsPerformed())

	for presIndex, PresContext := range pdu.AssocRQ.GetPresContexts() {
		logrus.Info("ASSOC-RQ: PresentationContext", "Index", presIndex)

		sopClass := sopclass.GetSOPClassFromUID(PresContext.GetAbstractSyntax().GetUID())
		logrus.Info("ASSOC-RQ: \tAbstractContext", "UID", sopClass.UID, "Description", sopClass.Description)
		for _, TransferSyn := range PresContext.GetTransferSyntaxes() {
			tsName := ""
			transferSyntax := transfersyntax.GetTransferSyntaxFromUID(TransferSyn.GetUID())
			if transferSyntax != nil {
				tsName = transferSyntax.Description
			}
			logrus.Info("ASSOC-RQ: \tTransferSynxtax:", "UID", TransferSyn.GetUID(), "Description", tsName)
		}

		PresContextAccept := NewPresentationContextAccept()
		PresContextAccept.SetResult(4)
		PresContextAccept.SetAbstractSyntax(PresContext.GetAbstractSyntax().GetUID())
		TS := ""
		for _, TrnSyntax := range PresContext.GetTransferSyntaxes() {
			TS = TrnSyntax.GetUID()
			break
		}
		if TS == "" {
			TS = transfersyntax.ImplicitVRLittleEndian.UID
		}

		PresContextAccept.SetResult(0)
		PresContextAccept.SetTransferSyntax(TS)
		PresContextAccept.SetPresentationContextID(PresContext.GetPresentationContextID())
		pdu.AcceptedPresentationContexts = append(pdu.AcceptedPresentationContexts, PresContextAccept)
		pdu.AssocAC.AddPresContextAccept(PresContextAccept)
	}

	if len(pdu.AcceptedPresentationContexts) > 0 {
		MaxSubLength := NewMaximumSubLength()
		UserInfo := NewUserInformation()

		MaxSubLength.SetMaximumLength(maxPduLength)
		UserInfo.SetImpClassUID(imp.GetImpClassUID())
		UserInfo.SetImpVersionName(imp.GetImpVersion())
		UserInfo.SetMaxSubLength(MaxSubLength)
		pdu.AssocAC.SetUserInformation(UserInfo)
		return pdu.AssocAC.Write(rw)
	}

	logrus.Info("pduservice::InterogateAAssociateRQ - No valid AcceptedPresentationContexts")
	return pdu.AssocRJ.Write(rw)
}

func (pdu *pduService) parseDCMIntoRaw(DCO *media.DcmObj) bool {
	pdu.Pdata.Buffer.WriteObj(DCO)
	return true
}

func (pdu *pduService) parseRawVRIntoDCM(DCO *media.DcmObj) bool {
	TrnSyntax := pdu.GetTransferSyntax(pdu.Pdata.PresentationContextID)
	if TrnSyntax == nil {
		logrus.Info("pduservice::ParseRawVRIntoDCM - Transfer syntax length is 0")
		return false
	}
	DCO.SetTransferSyntax(TrnSyntax)
	pdu.Pdata.Buffer.SetPosition(0)
	return pdu.Pdata.Buffer.ReadObj(DCO) == nil
}

func (pdu *pduService) readPDU() error {
	return pdu.ms.ReadFully(pdu.readWriter, int(pdu.pdulength)-4)
}

func (pdu *pduService) WriteResp(rqCommand uint16, DCO, ddo *media.DcmObj, status ...uint16) error {
	leDSType := dicomstatus.CommandDataSetTypeNull
	if ddo != nil && ddo.TagCount() > 0 {
		leDSType = dicomstatus.CommandDataSetTypeNonNull
		defer pdu.Write(ddo, 0x00)
	}

	DCOR := media.NewEmptyDCMObj()
	DCOR.SetTransferSyntax(DCO.GetTransferSyntax())
	DCOR.WriteString(tags.AffectedSOPClassUID, DCO.GetString(tags.AffectedSOPClassUID))
	DCOR.WriteUint16(tags.CommandField, rqCommand+dicomcommand.Offset)
	DCOR.WriteUint16(tags.MessageIDBeingRespondedTo, DCO.GetUShort(tags.MessageID))
	DCOR.WriteUint16(tags.CommandDataSetType, leDSType)
	DCOR.WriteUint16(tags.Status, status[0])
	DCOR.WriteString(tags.AffectedSOPInstanceUID, DCO.GetString(tags.AffectedSOPInstanceUID))
	if len(status) == 4 {
		DCOR.WriteUint16(tags.NumberOfRemainingSuboperations, status[1])
		DCOR.WriteUint16(tags.NumberOfCompletedSuboperations, status[2])
		DCOR.WriteUint16(tags.NumberOfFailedSuboperations, status[3])
	}
	return pdu.Write(DCOR, 0x01)
}

func (pdu *pduService) WriteRQ(rqCommand uint16, ddo *media.DcmObj, moveDst ...string) error {
	pdu.commandField = rqCommand
	moveDst = append(moveDst, "")
	leDSType := dicomstatus.CommandDataSetTypeNull
	if ddo != nil && ddo.TagCount() > 0 {
		leDSType = dicomstatus.CommandDataSetTypeNonNull
		defer pdu.Write(ddo, 0x00)
	}
	sopClassUID := ddo.GetString(tags.SOPClassUID)
	if sopClassUID == "" {
		for _, presContext := range pdu.GetAAssociationRQ().GetPresContexts() {
			sopClassUID = presContext.GetAbstractSyntax().GetUID()
		}
	}
	dco := media.NewEmptyDCMObj()
	dco.SetTransferSyntax(ddo.GetTransferSyntax())
	dco.WriteString(tags.AffectedSOPClassUID, sopClassUID)
	dco.WriteUint16(tags.CommandField, rqCommand)
	dco.WriteUint16(tags.MessageID, Uniq16odd())
	dco.WriteString(tags.MoveDestination, moveDst[0])
	dco.WriteUint16(tags.Priority, priority.Medium)
	dco.WriteUint16(tags.CommandDataSetType, leDSType)
	dco.WriteString(tags.AffectedSOPInstanceUID, ddo.GetString(tags.SOPInstanceUID))
	return pdu.Write(dco, 0x01)
}

func (pdu *pduService) ReadResp(pending ...*int) (ddo *media.DcmObj, status uint16, err error) {
	status = dicomstatus.FailureUnableToProcess
	dco, err := pdu.NextPDU()
	if err != nil {
		return
	}
	resp := pdu.commandField + dicomcommand.Offset
	switch resp {
	case dicomcommand.CEchoResponse, dicomcommand.CStoreResponse, dicomcommand.CFindResponse, dicomcommand.CMoveResponse:
	default:
		err = fmt.Errorf("command not found: %v", resp)
		return
	}
	if dco.GetUShort(tags.CommandDataSetType) != dicomstatus.CommandDataSetTypeNull {
		ddo, err = pdu.NextPDU()
		if len(pending) == 1 {
			*pending[0] = int(dco.GetUShort(tags.NumberOfRemainingSuboperations))
		}
	}
	return ddo, dco.GetUShort(tags.Status), err
}
