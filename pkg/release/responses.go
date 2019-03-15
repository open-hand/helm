/*
Copyright The Helm Authors.
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

package release

// GetReleaseStatusResponse is the response indicating the status of the named release.
type GetReleaseStatusResponse struct {
	// Name is the name of the release.
	Name string `json:"name,omitempty"`
	// Info contains information about the release.
	Info *Info `json:"info,omitempty"`
	// Namespace the release was released into
	Namespace string `json:"namespace,omitempty"`
}

// UninstallReleaseResponse represents a successful response to an uninstall request.
type UninstallReleaseResponse struct {
	// Release is the release that was marked deleted.
	Release *Release `json:"release,omitempty"`
	// Info is an uninstall message
	Info string `json:"info,omitempty"`
}

// TestReleaseResponse represents a message from executing a test
type TestReleaseResponse struct {
	Msg    string        `json:"msg,omitempty"`
	Status TestRunStatus `json:"status,omitempty"`
}