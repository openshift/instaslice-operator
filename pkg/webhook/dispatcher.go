package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/klog/v2"
)

const validContentType string = "application/json"

var (
	scheme          = runtime.NewScheme()
	admissionCodecs = serializer.NewCodecFactory(scheme)
)

// Dispatcher struct
type Dispatcher struct {
	hook Webhook
}

// NewDispatcher new dispatcher
func NewDispatcher(hook Webhook) *Dispatcher {
	return &Dispatcher{
		hook: hook,
	}
}

// HandleRequest http request
func (d *Dispatcher) HandleRequest(w http.ResponseWriter, r *http.Request) {
	klog.Info("Handling request", "request", r.RequestURI)
	_, err := url.Parse(r.RequestURI)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		klog.Error(err, "Couldn't parse request %s", r.RequestURI)
		SendResponse(w, admissionctl.Errored(http.StatusBadRequest, err))
		return
	}

	request, _, err := ParseHTTPRequest(r)
	// Problem parsing an AdmissionReview, so use BadRequest HTTP status code
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		klog.Error(err, "Error parsing HTTP Request Body")
		SendResponse(w, admissionctl.Errored(http.StatusBadRequest, err))
		return
	}

	SendResponse(w, d.hook.Authorized(request))
}

// SendResponse Send the AdmissionReview.
func SendResponse(w io.Writer, resp admissionctl.Response) {
	encoder := json.NewEncoder(w)
	responseAdmissionReview := admissionv1.AdmissionReview{
		Response: &resp.AdmissionResponse,
	}
	responseAdmissionReview.APIVersion = admissionv1.SchemeGroupVersion.String()
	responseAdmissionReview.Kind = "AdmissionReview"
	err := encoder.Encode(responseAdmissionReview)
	if err != nil {
		klog.Error(err, "Failed to encode Response", "response", resp)
		SendResponse(w, admissionctl.Errored(http.StatusInternalServerError, err))
	}
}

func ParseHTTPRequest(r *http.Request) (admissionctl.Request, admissionctl.Response, error) {
	var resp admissionctl.Response
	var req admissionctl.Request
	var err error
	var body []byte
	if r.Body != nil {
		if body, err = io.ReadAll(r.Body); err != nil {
			resp = admissionctl.Errored(http.StatusBadRequest, err)
			return req, resp, err
		}
	} else {
		err := errors.New("request body is nil")
		resp = admissionctl.Errored(http.StatusBadRequest, err)
		return req, resp, err
	}
	if len(body) == 0 {
		err := errors.New("request body is empty")
		resp = admissionctl.Errored(http.StatusBadRequest, err)
		return req, resp, err
	}
	contentType := r.Header.Get("Content-Type")
	if contentType != validContentType {
		err := fmt.Errorf("contentType=%s, expected application/json", contentType)
		resp = admissionctl.Errored(http.StatusBadRequest, err)
		return req, resp, err
	}
	ar := admissionv1.AdmissionReview{}
	if _, _, err := admissionCodecs.UniversalDeserializer().Decode(body, nil, &ar); err != nil {
		resp = admissionctl.Errored(http.StatusBadRequest, err)
		return req, resp, err
	}

	if ar.Request == nil {
		err = fmt.Errorf("no request in request body")
		resp = admissionctl.Errored(http.StatusBadRequest, err)
		return req, resp, err
	}
	resp.UID = ar.Request.UID
	req = admissionctl.Request{
		AdmissionRequest: *ar.Request,
	}
	return req, resp, nil
}

func (d *Dispatcher) HandleReadiness(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
}

func (d *Dispatcher) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
}
