package tcglog

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

func makeDefaultFormatter(s fmt.State, f rune) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%%")
	for _, flag := range [...]int{'+', '-', '#', ' ', '0'} {
		if s.Flag(flag) {
			fmt.Fprintf(&builder, "%c", flag)
		}
	}
	if width, ok := s.Width(); ok {
		fmt.Fprintf(&builder, "%d", width)
	}
	if prec, ok := s.Precision(); ok {
		fmt.Fprintf(&builder, ".%d", prec)
	}
	fmt.Fprintf(&builder, "%c", f)
	return builder.String()
}

func hash(data []byte, alg AlgorithmId) []byte {
	switch alg {
	case AlgorithmSha1:
		h := sha1.Sum(data)
		return h[:]
	case AlgorithmSha256:
		h := sha256.Sum256(data)
		return h[:]
	case AlgorithmSha384:
		h := sha512.Sum384(data)
		return h[:]
	case AlgorithmSha512:
		h := sha512.Sum512(data)
		return h[:]
	default:
		panic("Unhandled algorithm")
	}
}

type PCRArgList []PCRIndex

func (l *PCRArgList) String() string {
	var builder strings.Builder
	for i, pcr := range *l {
		if i > 0 {
			fmt.Fprintf(&builder, ", ")
		}
		fmt.Fprintf(&builder, "%d", pcr)
	}
	return builder.String()
}

func (l *PCRArgList) Set(value string) error {
	v, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return err
	}
	*l = append(*l, PCRIndex(v))
	return nil
}

func contains(slice interface{}, elem interface{}) bool {
	sv := reflect.ValueOf(slice)
	if sv.Kind() != reflect.Slice {
		panic(fmt.Sprintf("Invalid kind - expected a slice (got %s)", sv.Kind()))
	}
	if sv.Type().Elem() != reflect.ValueOf(elem).Type() {
		panic(fmt.Sprintf("Type mismatch (%s vs %s)", sv.Type().Elem(), reflect.ValueOf(elem).Type()))
	}
	for i := 0; i < sv.Len(); i++ {
		if sv.Index(i).Interface() == elem {
			return true
		}
	}
	return false
}
