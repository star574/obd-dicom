package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/star574/obd-dicom/dictionary/tags"
	"github.com/star574/obd-dicom/dictionary/transfersyntax"
	"github.com/star574/obd-dicom/media"
	"github.com/star574/obd-dicom/network"
	"github.com/star574/obd-dicom/network/dicomstatus"
	"github.com/star574/obd-dicom/utils"
)

var destination *network.Destination
var version string

func main() {
	log.Printf("Starting odb-dicom %s\n\n", version)

	media.InitDict()

	hostName := flag.String("host", "localhost", "Destination host name or IP")
	calledAE := flag.String("calledae", "DICOM_SCP", "AE of the destination")
	callingAE := flag.String("callingae", "DICOM_SCU", "AE of the client")
	port := flag.Int("port", 1040, "Port of the destination system")

	studyUID := flag.String("studyuid", "", "Study UID to be added to request")

	destinationAE := flag.String("destinationae", "", "AE of the destination for a C-Move request")

	fileName := flag.String("file", "", "DICOM file to be sent")

	cecho := flag.Bool("cecho", false, "Send C-Echo to the destination")
	cfind := flag.Bool("cfind", false, "Send C-Find request to the destination")
	cfindWorkList := flag.Bool("cfindWorklist", false, "Send C-Find Modality Worklist request to the destination")
	cmove := flag.Bool("cmove", false, "Send C-Move request to the destination")
	cstore := flag.Bool("cstore", false, "Sends a C-Store request to the destination")
	dump := flag.Bool("dump", false, "Dump contents of DICOM file to stdout")

	modify := flag.String("modify", "", "Modify dicom tag. Eg: PatientName=test,PatientBirthDate=123")

	transcode := flag.Bool("transcode", false, "Transcode contents of DICOM file to new Transfersyntax")
	supportedTS := "TransferSyntax file to be converted. Supported: \n"
	for _, ts := range transfersyntax.SupportedTransferSyntaxes {
		supportedTS += fmt.Sprintf("%s \n", ts.Name)
	}
	transferSyntax := flag.String("ts", "", supportedTS)

	datastore := flag.String("datastore", "", "Directory to use as SCP storage")

	startSCP := flag.Bool("scp", false, "Start a SCP")

	flag.Parse()

	if *startSCP {
		if *datastore == "" {
			log.Fatalln("datastore is required for scp")
		}

		if *calledAE == "" {
			log.Fatalln("calledae is required for scp")
		}
		scp := network.NewSCP(*port)

		scp.OnAssociationRequest(func(request *network.AAssociationRQ) bool {
			called := request.GetCalledAE()
			return *calledAE == called
		})

		scp.OnCFindRequest(func(request *network.AAssociationRQ, query *media.DcmObj) ([]*media.DcmObj, uint16) {
			query.DumpTags()
			results := make([]*media.DcmObj, 0)
			for i := 0; i < 10; i++ {
				results = append(results, utils.GenerateCFindRequest())
			}
			return results, dicomstatus.Success
		})

		scp.OnCMoveRequest(func(request *network.AAssociationRQ, moveLevel string, query *media.DcmObj, moveDst *network.Destination) ([]string, uint16) {
			query.DumpTags()
			return []string{}, dicomstatus.Success
		})

		scp.OnCStoreRequest(func(request *network.AAssociationRQ, data *media.DcmObj) uint16 {
			log.Printf("INFO, C-Store recieved %s", data.GetString(tags.SOPInstanceUID))
			directory := filepath.Join(*datastore, data.GetString(tags.PatientID), data.GetString(tags.StudyInstanceUID), data.GetString(tags.SeriesInstanceUID))
			os.MkdirAll(directory, 0755)

			path := filepath.Join(directory, data.GetString(tags.SOPInstanceUID)+".dcm")

			err := data.WriteToFile(path)
			if err != nil {
				log.Printf("ERROR: There was an error saving %s : %s", path, err.Error())
			}
			return dicomstatus.Success
		})

		err := scp.Start()
		if err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}

	destination = &network.Destination{
		Name:      *hostName,
		HostName:  *hostName,
		CalledAE:  *calledAE,
		CallingAE: *callingAE,
		Port:      *port,
		IsCFind:   true,
		IsCStore:  true,
		IsMWL:     true,
		IsTLS:     false,
	}

	if *cecho {
		scu := network.NewSCU(destination)
		err := scu.EchoSCU(30)
		if err != nil {
			log.Fatalln(err)
		}
		log.Println("CEcho was successful")
	}
	if *cfind || *cfindWorkList {
		request := utils.DefaultCFindRequest()
		scu := network.NewSCU(destination)
		scu.SetOnCFindResult(func(result *media.DcmObj) {
			log.Printf("Found study %s\n", result.GetString(tags.StudyInstanceUID))
			result.DumpTags()
		})
		var modes []network.FindMode
		if *cfindWorkList {
			modes = append(modes, network.FindWorklist)
		}
		count, status, err := scu.FindSCU(request, 0, modes...)
		if err != nil {
			log.Fatalln(err)
		}

		log.Println("CFind was successful")
		log.Printf("Found %d results with status %d\n\n", count, status)
		os.Exit(0)
	}
	if *cmove {
		if *destinationAE == "" {
			log.Fatalln("destinationae is required for a C-Move")
		}
		if *studyUID == "" {
			log.Fatalln("studyuid is required for a C-Move")
		}

		request := utils.DefaultCMoveRequest(*studyUID)

		scu := network.NewSCU(destination)
		_, err := scu.MoveSCU(*destinationAE, request, 0)
		if err != nil {
			log.Fatalln(err)
		}
		log.Println("CMove was successful")
		os.Exit(0)
	}
	if *cstore {
		if *fileName == "" {
			log.Fatalln("file is required for a C-Store")
		}
		scu := network.NewSCU(destination)
		err := scu.StoreSCU([]string{*fileName}, 0)
		if err != nil {
			log.Fatalln(err)
		}
		log.Printf("CStore of %s was successful", *fileName)
		os.Exit(0)
	}
	if *dump {
		if *fileName == "" {
			log.Fatalln("file is required for a dump")
		}
		obj, err := media.NewDCMObjFromFile(*fileName)
		if err != nil {
			log.Panicln(err)
		}
		obj.DumpTags()
		os.Exit(0)
	}
	if *transcode {
		if *fileName == "" {
			log.Fatalln("file is required for transcode")
		}
		if *transferSyntax == "" {
			log.Fatalln("transferSyntax is required for transcode")
		}
		obj, err := media.NewDCMObjFromFile(*fileName)
		if err != nil {
			log.Panicln(err)
		}
		if err = obj.ChangeTransferSynx(transfersyntax.GetTransferSyntaxFromName(*transferSyntax)); err != nil {
			log.Panicln(err)
		}
		log.Printf("Transcode ok. Writing file")
		obj.WriteToFile(*fileName)
		os.Exit(0)
	}
	if *modify != "" {
		if *fileName == "" {
			log.Fatalln("file is required for modify")
		}
		obj, err := media.NewDCMObjFromFile(*fileName)
		parts := strings.Split(*modify, ",")
		for _, part := range parts {
			p := strings.Split(part, "=")
			if len(p) == 2 {
				tagName := p[0]
				value := p[1]

				if err != nil {
					log.Fatalln(err)
				}
				if tag := tags.GetTagFromName(tagName); tag != nil {
					obj.WriteString(tag, value)
					log.Printf("write tag %s = %s ok", tag.Name, value)
				}
			}
		}
		obj.WriteToFile(*fileName)
		os.Exit(0)
	}
}
