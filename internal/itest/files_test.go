package itest

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"vetapp-backend/internal/models"
)

// upload posts a small PDF as multipart/form-data.
func upload(t *testing.T, as uint, path string) resp {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "ანალიზი.pdf")
	fw.Write([]byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF"))
	mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token(t, as))
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{Code: res.StatusCode, Body: b}
}

func TestClinicLabFiles(t *testing.T) {
	reset(t)
	lab := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 116, SK: clinicA, Phone: "0", Date: "2026-09-24"})
	base := fmt.Sprintf("/api/pets/%d/procedures/%d/files", petA, lab.ID)

	// Another clinic cannot attach to, list, or read this record's files.
	expect(t, upload(t, vetB, base), http.StatusNotFound)

	r := upload(t, vetA, base)
	expect(t, r, http.StatusCreated)
	var f struct{ ID uint }
	r.json(t, &f)

	r = call(t, vetA, "GET", base, nil)
	expect(t, r, http.StatusOK)
	var list []struct{ FileName string }
	r.json(t, &list)
	if len(list) != 1 || list[0].FileName != "ანალიზი.pdf" {
		t.Errorf("list = %+v", list)
	}
	expect(t, call(t, vetB, "GET", base, nil), http.StatusNotFound)
	expect(t, call(t, vetA, "GET", fmt.Sprintf("%s/%d", base, f.ID), nil), http.StatusOK)
	expect(t, call(t, vetB, "GET", fmt.Sprintf("%s/%d", base, f.ID), nil), http.StatusNotFound)

	// The procedure must belong to the pet in the path.
	expect(t, upload(t, vetA, fmt.Sprintf("/api/pets/%d/procedures/%d/files", petB, lab.ID)), http.StatusNotFound)

	expect(t, call(t, vetA, "DELETE", fmt.Sprintf("%s/%d", base, f.ID), nil), http.StatusOK)
}

func TestOwnerFilesStillWork(t *testing.T) {
	reset(t)
	p := insertProc(t, models.Procedure{UUID: fmt.Sprint(petA), TP: 116, Date: "2026-09-24"})
	base := fmt.Sprintf("/api/owner/pets/%d/procedures/%d/files", petA, p.ID)
	expect(t, upload(t, owner, base), http.StatusCreated)
	r := call(t, owner, "GET", base, nil)
	expect(t, r, http.StatusOK)
	// A procedure id that belongs to another pet is refused.
	other := insertProc(t, models.Procedure{UUID: "123", TP: 116, Date: "2026-09-24"})
	expect(t, upload(t, owner, fmt.Sprintf("/api/owner/pets/%d/procedures/%d/files", petA, other.ID)), http.StatusNotFound)
}
