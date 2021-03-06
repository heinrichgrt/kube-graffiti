/*
Copyright (C) 2018 Expedia Group.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/HotelsDotCom/kube-graffiti/pkg/log"
	admission "k8s.io/api/admission/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
)

// graffitHandler contains the context needed within our http handler without using global variables
// It satisfies the http.Handler interface
type graffitiHandler struct {
	tagmap map[string]graffitiMutator
}

// graffitiMutator interface allows us to mock out for testing.
type graffitiMutator interface {
	MutateAdmission(req *admission.AdmissionRequest) *admission.AdmissionResponse
}

func newGraffitiHandler() graffitiHandler {
	return graffitiHandler{
		tagmap: make(map[string]graffitiMutator),
	}
}

// addRule allows us to add rules to a handler without relying on its implementation
func (h graffitiHandler) addRule(path string, rule graffitiMutator) {
	h.tagmap[path] = rule
}

// ServeHTTP performs the basic validation that we received a valid AdmissionReview request.
// It looks up the graffiti tag associated with a given webhook path (the URL) and calls its 'mutate' method to
func (h graffitiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Path
	mylog := log.ComponentLogger(componentName, "graffitiHandler-ServeHTTP")
	reqLog := mylog.With().Str("url", url).Str("host", r.Host).Str("method", r.Method).Str("ua", r.UserAgent()).Str("remote", r.RemoteAddr).Logger()
	reqLog.Debug().Msg("webhook triggered, performing the mutating admission review")

	var body []byte
	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
		}
	}

	// verify the http method is a POST
	if r.Method != "POST" {
		reqLog.Error().Str("method", r.Method).Msg("received invalid method, expecting POST")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusMethodNotAllowed)
		io.WriteString(w, `invalid http method`)
		return
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		reqLog.Error().Str("content-type", contentType).Msg("bad content-type - not application/json")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `invalid request - payload is not json`)
		return
	}

	reqLog.Debug().Str("request-body", string(body)).Msg("request json received")

	ar := admission.AdmissionReview{}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&ar); err != nil {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `The request does not contain a valid AdmissionReview object`)
		reqLog.Error().Err(err).Msg("failed to decode AdmissionReview request")
		return
	}
	reqLog.Debug().Msg("unmarshalled request")

	reviewResponse := &admission.AdmissionResponse{}
	// check that we have a Graffiti matching this URL path...
	if mutator, ok := h.tagmap[url]; !ok {
		reqLog.Warn().Str("path", url).Msg("can't find a grafitti rule for path")
		reviewResponse.Allowed = true
	} else {
		reqLog.Debug().Str("path", url).Msg("found a graffiti rule for path")
		// call the Mutate method associated with this rule
		reviewResponse = mutator.MutateAdmission(ar.Request)
	}

	response := admission.AdmissionReview{}
	if reviewResponse != nil {
		response.Response = reviewResponse
		response.Response.UID = ar.Request.UID
	}
	// reset the Object and OldObject, they are not needed in a response.
	ar.Request.Object = runtime.RawExtension{}
	ar.Request.OldObject = runtime.RawExtension{}

	reqLog.Debug().Msg("writing AdmissionReview response")
	resp, err := json.Marshal(response)
	if err != nil {
		mylog.Error().Err(err).Msg("failed to marshal AdmissionReview response")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(resp); err != nil {
		reqLog.Error().Err(err).Msg("failed to write the http response")
	}
	reqLog.Debug().Str("json", string(resp)).Msg("webhook response")
}
