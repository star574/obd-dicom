package network

import (
	"errors"
	"fmt"
	"github.com/sirupsen/logrus"
	"strconv"

	"github.com/star574/obd-dicom/dictionary/sopclass"
	"github.com/star574/obd-dicom/dictionary/transfersyntax"
	"github.com/star574/obd-dicom/media"
	"github.com/star574/obd-dicom/network/dicomcommand"
	"github.com/star574/obd-dicom/network/dicomstatus"
)

type scu struct {
	destination    *Destination
	onCFindResult  func(result *media.DcmObj)
	onCMoveResult  func(result *media.DcmObj)
	onCStoreResult func(pending, completed, failed uint16) error
}

type FindMode uint8

const (
	FindWorklist FindMode = iota
	FindPatient
	FindStudy
	FINDPatientStudyOnly
)

// NewSCU - Creates an interface to scu
func NewSCU(destination *Destination) *scu {
	return &scu{
		destination: destination,
	}
}

func (d *scu) EchoSCU(timeout int) error {
	pdu := newPDUService()
	if err := d.openAssociation(pdu, []*sopclass.SOPClass{sopclass.Verification}, []string{}, timeout); err != nil {
		return err
	}
	defer pdu.Close()
	if err := pdu.WriteRQ(dicomcommand.CEchoRequest, media.NewEmptyDCMObj()); err != nil {
		return err
	}
	if _, _, err := pdu.ReadResp(); err != nil {
		return err
	}
	return nil
}

func (d *scu) FindSCU(Query *media.DcmObj, timeout int, mode ...FindMode) (int, uint16, error) {
	results := 0
	status := dicomstatus.Warning

	var abstractSyntax *sopclass.SOPClass
	mode = append(mode, FindStudy)
	switch mode[0] {
	case FindWorklist:
		abstractSyntax = sopclass.ModalityWorklistInformationModelFind
	case FindPatient:
		abstractSyntax = sopclass.PatientRootQueryRetrieveInformationModelFind
	case FINDPatientStudyOnly:
		abstractSyntax = sopclass.PatientStudyOnlyQueryRetrieveInformationModelFind
	default:
		abstractSyntax = sopclass.StudyRootQueryRetrieveInformationModelFind
	}

	pdu := newPDUService()
	if err := d.openAssociation(pdu, []*sopclass.SOPClass{abstractSyntax}, []string{}, timeout); err != nil {
		return results, status, err
	}
	defer pdu.Close()
	if err := pdu.WriteRQ(dicomcommand.CFindRequest, Query); err != nil {
		return results, status, err
	}
	for status != dicomstatus.Success {
		ddo, s, err := pdu.ReadResp()
		status = s
		if err != nil {
			return results, status, err
		}
		if (status == dicomstatus.Pending) || (status == dicomstatus.PendingWithWarnings) {
			results++
			if d.onCFindResult != nil {
				d.onCFindResult(ddo)
			} else {
				logrus.Warn("No onCFindResult event found")
			}
		}
	}

	return results, status, nil
}

func (d *scu) MoveSCU(destAET string, Query *media.DcmObj, timeout int) (uint16, error) {
	var pending int
	status := dicomstatus.Pending

	pdu := newPDUService()
	if err := d.openAssociation(pdu, []*sopclass.SOPClass{sopclass.StudyRootQueryRetrieveInformationModelFind, sopclass.StudyRootQueryRetrieveInformationModelMove}, []string{}, timeout); err != nil {
		return dicomstatus.FailureUnableToProcess, err
	}
	defer pdu.Close()
	if err := pdu.WriteRQ(dicomcommand.CMoveRequest, Query, destAET); err != nil {
		return dicomstatus.FailureUnableToProcess, err
	}

	for status == dicomstatus.Pending {
		ddo, s, err := pdu.ReadResp(&pending)
		status = s
		if err != nil {
			return dicomstatus.FailureUnableToProcess, err
		}
		if d.onCMoveResult != nil {
			d.onCMoveResult(ddo)
		} else {
			logrus.Warn("No onCMoveResult event found")
		}
	}
	return status, nil
}

func (d *scu) StoreSCU(FileNames []string, timeout int, transferSyntaxes ...string) error {
	var failed, completed, pending uint16
	pdu := newPDUService()
	if len(transferSyntaxes) == 0 {
		transferSyntaxes = append(transferSyntaxes, transfersyntax.JPEGLosslessSV1.UID, transfersyntax.ImplicitVRLittleEndian.UID)
	}
	if err := d.openAssociation(pdu, sopclass.DcmShortSCUStorageSOPClassUIDs, transferSyntaxes, timeout); err != nil {
		return err
	}
	defer pdu.Close()
	for index, FileName := range FileNames {
		pending = uint16(len(FileNames) - index - 1)
		if err := d.cstore(pdu, FileName); err != nil {
			failed++
			logrus.Warn("StoreSCU", "File", FileName, "Error", err.Error())
		} else {
			completed++
		}
		if d.onCStoreResult != nil {
			if err := d.onCStoreResult(pending, completed, failed); err != nil {
				return err
			}
		}
	}
	logrus.Info("StoreSCU", "Completed", completed, "Failed", failed)
	return nil
}

func (d *scu) cstore(pdu *pduService, FileName string) error {
	DDO, err := media.NewDCMObjFromFile(FileName)
	if err != nil {
		return err
	}
	if err = getCStoreError(d.writeStoreRQ(pdu, DDO)); err != nil {
		return err
	}
	_, status, err := pdu.ReadResp()
	return getCStoreError(status, err)
}

func getCStoreError(status uint16, err error) error {
	if err != nil {
		return err
	}
	if status != dicomstatus.Success {
		return fmt.Errorf("serviceuser::StoreSCU, dimsec.CStoreReadRSP failed - %d", status)
	}
	return nil
}

func (d *scu) SetOnCFindResult(f func(result *media.DcmObj)) {
	d.onCFindResult = f
}

func (d *scu) SetOnCMoveResult(f func(result *media.DcmObj)) {
	d.onCMoveResult = f
}

func (d *scu) openAssociation(pdu *pduService, abstractSyntaxes []*sopclass.SOPClass, transferSyntaxes []string, timeout int) error {
	pdu.SetCallingAE(d.destination.CallingAE)
	pdu.SetCalledAE(d.destination.CalledAE)
	pdu.SetTimeout(timeout)

	Resetuniq()
	for _, syntax := range abstractSyntaxes {
		PresContext := NewPresentationContext()
		PresContext.SetAbstractSyntax(syntax.UID)
		for _, ts := range transferSyntaxes {
			PresContext.AddTransferSyntax(ts)
		}
		if len(transferSyntaxes) == 0 {
			PresContext.AddTransferSyntax(transfersyntax.ImplicitVRLittleEndian.UID)
		}
		pdu.AddPresContexts(PresContext)
	}

	return pdu.Connect(d.destination.HostName, strconv.Itoa(d.destination.Port))
}

func (d *scu) writeStoreRQ(pdu *pduService, DDO *media.DcmObj) (uint16, error) {
	status := dicomstatus.FailureUnableToProcess

	PCID := pdu.GetPresentationContextID()
	if PCID == 0 {
		return dicomstatus.FailureUnableToProcess, errors.New("serviceuser::WriteStoreRQ, PCID==0")
	}
	TrnSyntOUT := pdu.GetTransferSyntax(PCID)

	if TrnSyntOUT == nil {
		return dicomstatus.FailureUnableToProcess, errors.New("serviceuser::WriteStoreRQ, TrnSyntOut is empty")
	}

	if TrnSyntOUT.UID == DDO.GetTransferSyntax().UID {
		if err := pdu.WriteRQ(dicomcommand.CStoreRequest, DDO); err != nil {
			return status, err
		}
		return dicomstatus.Success, nil
	}
	logrus.Info("StoreSCU: Transcode.", "From", DDO.GetTransferSyntax().Description, "To", TrnSyntOUT.Description)
	DDO.ChangeTransferSynx(TrnSyntOUT)

	err := pdu.WriteRQ(dicomcommand.CStoreRequest, DDO)
	if err != nil {
		return dicomstatus.FailureUnableToProcess, err
	}
	return dicomstatus.Success, nil
}
