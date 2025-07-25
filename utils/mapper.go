package utils

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/star574/obd-dicom/dictionary/tags"
	"github.com/star574/obd-dicom/media"
)

var key string
var struct2Dicom bool

// Map dicom object to struct. keyword = dicom by defaut
func MapDicomDataToStruct(dicomDataset *media.DcmObj, targetStruct any, keyword ...string) (err error) {
	return mapping(dicomDataset, targetStruct, false, keyword...)
}

func mapping(dicomDataset *media.DcmObj, targetStruct any, toDicom bool, keyword ...string) error {
	v := reflect.ValueOf(targetStruct)
	t := reflect.TypeOf(targetStruct).Kind()
	if t != reflect.Ptr {
		return fmt.Errorf("targerStruct must be a pointer")
	}
	key = "dicom"
	if len(keyword) > 0 {
		key = keyword[0]
	}
	struct2Dicom = toDicom
	recursiveFill(dicomDataset, v.Elem())
	return nil
}

// Map struct to dicom object, do nothing if tag does not exist
func MapToDicom(in any, obj *media.DcmObj, keyword ...string) (err error) {
	return mapping(obj, in, true, keyword...)
}

// recursiveFill analyze recursively the target structure and find corresponding Dicom value in the dataset.
func recursiveFill(dataset *media.DcmObj, targetStructure reflect.Value) {
	if targetStructure.Kind() == reflect.Struct {
		targetType := targetStructure.Type()
		for i := 0; i < targetStructure.NumField(); i++ {
			field := targetType.Field(i)
			fieldName := field.Name
			fieldType := field.Type
			groupElem := field.Tag.Get(key)
			switch fieldType.Kind() {
			case reflect.Struct:
				recursiveFill(dataset, targetStructure.Field(i))
			case reflect.Slice:
				goDeeper(fieldType, dataset, targetStructure, fieldName, groupElem)
			default:
				fillElement(fieldType, dataset, targetStructure, fieldName, groupElem)
			}

		}
	}
}

// goDeeper is called when a struct field is a Slice of another type, either a base type or a struct type
func goDeeper(fieldType reflect.Type, dataset *media.DcmObj, targetStructure reflect.Value, fieldName, groupElem string) {
	sliceElement := fieldType.Elem()
	if sliceElement.Kind() == reflect.Struct {
		s := reflect.New(sliceElement)
		recursiveFill(dataset, s.Elem())
		f := targetStructure.FieldByName(fieldName)
		f = reflect.MakeSlice(f.Type(), 1, 1)
		f.Index(0).Set(s.Elem())
		targetStructure.FieldByName(fieldName).Set(f)
	} else {
		fillElement(fieldType, dataset, targetStructure, fieldName, groupElem)
	}
}

func fillElement(fieldType reflect.Type, dataset *media.DcmObj, targetStructure reflect.Value, fieldName, groupElem string) {
	var group, elem string
	dicomFieldTags := strings.Split(groupElem, ",")
	if len(dicomFieldTags) == 2 {
		group = dicomFieldTags[0]
		elem = dicomFieldTags[1]
	}
	if group == "" || elem == "" {
		return
	}
	groupHex, err := strconv.ParseInt("0x"+group, 0, 16)
	if err != nil {
		return
	}
	elemHex, err := strconv.ParseInt("0x"+elem, 0, 16)
	if err != nil {
		return
	}
	tag := &tags.Tag{Group: uint16(groupHex), Element: uint16(elemHex)}
	switch fieldType.Kind() {
	case reflect.String:
		if struct2Dicom {
			if dataset.GetTag(tag) != nil {
				dataset.WriteString(tag, targetStructure.FieldByName(fieldName).String())
			}
		} else {
			targetStructure.FieldByName(fieldName).SetString(dataset.GetString(tag))
		}
	case reflect.Uint8, reflect.Uint16:
		if struct2Dicom {
			if dataset.GetTag(tag) != nil {
				dataset.WriteUint16(tag, uint16(targetStructure.FieldByName(fieldName).Uint()))
			}
		} else {
			targetStructure.FieldByName(fieldName).SetUint(uint64(dataset.GetUShort(tag)))
		}
	}
}
