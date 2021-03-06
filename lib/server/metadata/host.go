/*
 * Copyright 2018-2020, CS Systemes d'Information, http://csgroup.eu
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package metadata

import (
	"fmt"

	"github.com/graymeta/stow"
	"github.com/sirupsen/logrus"

	"github.com/CS-SI/SafeScale/lib/utils/debug"

	"github.com/CS-SI/SafeScale/lib/server/iaas"
	"github.com/CS-SI/SafeScale/lib/server/iaas/abstract"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
	"github.com/CS-SI/SafeScale/lib/utils/metadata"
	"github.com/CS-SI/SafeScale/lib/utils/retry"
	"github.com/CS-SI/SafeScale/lib/utils/serialize"
	"github.com/CS-SI/SafeScale/lib/utils/temporal"
)

const (
	// hostsFolderName is the technical name of the container used to store networks info
	hostsFolderName = "hosts"
)

// Host links Object Storage folder and Network
type Host struct {
	item *metadata.Item
	name *string
	id   *string
}

// NewHost creates an instance of api.Host
func NewHost(svc iaas.Service) (_ *Host, err error) {
	defer fail.OnPanic(&err)()

	aHost, err := metadata.NewItem(svc, hostsFolderName)
	if err != nil {
		return nil, err
	}

	return &Host{
		item: aHost,
	}, nil
}

func (mh *Host) OK() (bool, error) {
	if mh == nil {
		return false, fail.InvalidInstanceError()
	}

	if mh.id == nil && mh.name == nil {
		if mh.item == nil {
			return false, nil
		}

		if ok, err := mh.item.OK(); err != nil || !ok {
			return false, nil
		}
	}

	return true, nil
}

// Carry links an host instance to the Metadata instance
func (mh *Host) Carry(host *abstract.Host) (_ *Host, err error) {
	defer fail.OnPanic(&err)()

	if host == nil {
		return nil, fail.InvalidParameterError("host", "cannot be nil!")
	}
	if host.Properties == nil {
		host.Properties = serialize.NewJSONProperties("abstract")
	}
	mh.item.Carry(host)
	mh.name = &host.Name
	mh.id = &host.ID
	return mh, nil
}

// Get returns the Network instance linked to metadata
func (mh *Host) Get() (_ *abstract.Host, err error) {
	defer fail.OnPanic(&err)()

	if mh == nil {
		return nil, fail.InvalidInstanceError()
	}
	if mh.item == nil {
		return nil, fail.InvalidParameterError("mh.item", "cannot be nil")
	}

	gh := mh.item.Get().(*abstract.Host)
	return gh, nil
}

// Write updates the metadata corresponding to the host in the Object Storage
func (mh *Host) Write() (err error) {
	defer fail.OnPanic(&err)()

	if mh == nil {
		return fail.InvalidInstanceError()
	}
	if mh.item == nil {
		return fail.InvalidParameterError("m.item", "cannot be nil")
	}

	tracer := debug.NewTracer(nil, "('"+*mh.id+"')", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &err)()

	err = mh.item.WriteInto(ByNameFolderName, *mh.name)
	if err != nil {
		return err
	}
	return mh.item.WriteInto(ByIDFolderName, *mh.id)
}

// ReadByReference ...
func (mh *Host) ReadByReference(ref string) (err error) {
	defer fail.OnPanic(&err)()

	if mh == nil {
		return fail.InvalidInstanceError()
	}
	if mh.item == nil {
		return fail.InvalidInstanceContentError("mh.item", "cannot be nil")
	}
	if ref == "" {
		return fail.InvalidParameterError("ref", "cannot be empty string")
	}

	tracer := debug.NewTracer(nil, "("+ref+")", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogErrorWithLevel(tracer.TraceMessage(""), &err, logrus.TraceLevel)()

	var errors []error
	err1 := mh.mayReadByID(ref) // First read by ID ...
	if err1 != nil {
		errors = append(errors, err1)
	}

	err2 := mh.mayReadByName(ref) // ... then read by name if by id failed (no need to read twice if the 2 exist)
	if err2 != nil {
		errors = append(errors, err2)
	}

	if len(errors) == 2 {
		if err1 == stow.ErrNotFound && err2 == stow.ErrNotFound { // FIXME: Remove stow dependency
			return fail.NotFoundErrorWithCause(fmt.Sprintf("reference %s not found", ref), fail.ErrListError(errors))
		}

		if _, ok := err1.(fail.ErrNotFound); ok {
			if _, ok := err2.(fail.ErrNotFound); ok {
				return fail.NotFoundErrorWithCause(
					fmt.Sprintf("reference %s not found", ref), fail.ErrListError(errors),
				)
			}
		}

		return fail.ErrListError(errors)
	}

	return nil
}

// mayReadByID reads the metadata of a network identified by ID from Object Storage
// Doesn't log error or validate parameter by design; caller does that
func (mh *Host) mayReadByID(id string) (err error) {
	host := abstract.NewHost()
	err = mh.item.ReadFrom(
		ByIDFolderName, id, func(buf []byte) (serialize.Serializable, error) {
			err := host.Deserialize(buf)
			if err != nil {
				return nil, err
			}
			return host, nil
		},
	)
	if err != nil {
		return err
	}

	mh.id = &(host.ID)
	mh.name = &(host.Name)
	return nil
}

// mayReadByName reads the metadata of a host identified by name
// Doesn't log error or validate parameter by design; caller does that
func (mh *Host) mayReadByName(name string) (err error) {
	host := abstract.NewHost()
	err = mh.item.ReadFrom(
		ByNameFolderName, name, func(buf []byte) (serialize.Serializable, error) {
			err := host.Deserialize(buf)
			if err != nil {
				return nil, err
			}
			return host, nil
		},
	)
	if err != nil {
		return err
	}

	mh.name = &(host.Name)
	mh.id = &(host.ID)
	return nil
}

// ReadByID reads the metadata of a network identified by ID from Object Storage
func (mh *Host) ReadByID(id string) (err error) {
	defer fail.OnPanic(&err)()

	if mh == nil {
		return fail.InvalidInstanceError()
	}
	if mh.item == nil {
		return fail.InvalidInstanceContentError("mh.item", "cannot be nil")
	}
	if id == "" {
		return fail.InvalidParameterError("id", "cannot be empty string")
	}

	tracer := debug.NewTracer(nil, "("+id+")", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogErrorWithLevel(tracer.TraceMessage(""), &err, logrus.TraceLevel)()

	return mh.mayReadByID(id)
}

// ReadByName reads the metadata of a host identified by name
func (mh *Host) ReadByName(name string) (err error) {
	defer fail.OnPanic(&err)()

	if mh == nil {
		return fail.InvalidInstanceError()
	}
	if mh.item == nil {
		return fail.InvalidParameterError("mh.item", "cannot be nil")
	}
	if name == "" {
		return fail.InvalidParameterError("name", "cannot be empty string")
	}

	tracer := debug.NewTracer(nil, "('"+name+"')", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogErrorWithLevel(tracer.TraceMessage(""), &err, logrus.TraceLevel)()

	return mh.mayReadByName(name)
}

// Delete updates the metadata corresponding to the host
func (mh *Host) Delete() (err error) {
	defer fail.OnPanic(&err)()

	if mh == nil {
		return fail.InvalidInstanceError()
	}
	if mh.item == nil {
		return fail.InvalidParameterError("mh.item", "cannot be nil")
	}

	tracer := debug.NewTracer(nil, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogErrorWithLevel(tracer.TraceMessage(""), &err, logrus.TraceLevel)()

	err1 := mh.item.DeleteFrom(ByIDFolderName, *mh.id)
	err2 := mh.item.DeleteFrom(ByNameFolderName, *mh.name)

	if err1 != nil && err2 != nil {
		return fail.ErrListError([]error{err1, err2})
	}

	return nil
}

// Browse walks through host folder and executes a callback for each entries
func (mh *Host) Browse(callback func(*abstract.Host) error) (err error) {
	defer fail.OnPanic(&err)()

	if mh == nil {
		return fail.InvalidInstanceError()
	}
	if mh.item == nil {
		return fail.InvalidParameterError("mh.item", "cannot be nil")
	}

	tracer := debug.NewTracer(nil, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogErrorWithLevel(tracer.TraceMessage(""), &err, logrus.TraceLevel)()

	err = mh.item.BrowseInto(
		ByIDFolderName, func(buf []byte) error {
			host := abstract.NewHost()
			nerr := host.Deserialize(buf)
			if nerr != nil {
				return nerr
			}

			cav := callback(host)
			return cav
		},
	)

	return err
}

// SaveHost saves the Host definition in Object Storage
func SaveHost(svc iaas.Service, host *abstract.Host) (mh *Host, err error) {
	defer fail.OnPanic(&err)()

	if svc == nil {
		return nil, fail.InvalidParameterError("svc", "cannot be nil")
	}
	if host == nil {
		return nil, fail.InvalidParameterError("host", "cannot be nil")
	}

	tracer := debug.NewTracer(nil, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogErrorWithLevel(tracer.TraceMessage(""), &err, logrus.TraceLevel)()

	mh, err = NewHost(svc)
	if err != nil {
		return nil, err
	}

	ch, err := mh.Carry(host)
	if err != nil {
		return nil, err
	}

	err = ch.Write()
	if err != nil {
		return nil, err
	}

	return mh, nil
}

// RemoveHost removes the host definition from Object Storage
func RemoveHost(svc iaas.Service, host *abstract.Host) (err error) {
	defer fail.OnPanic(&err)()

	if svc == nil {
		return fail.InvalidParameterError("svc", "cannot be nil")
	}
	if host == nil {
		return fail.InvalidParameterError("host", "cannot be nil")
	}

	tracer := debug.NewTracer(nil, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogErrorWithLevel(tracer.TraceMessage(""), &err, logrus.TraceLevel)()

	mh, err := NewHost(svc)
	if err != nil {
		return err
	}

	ch, err := mh.Carry(host)
	if err != nil {
		return err
	}

	return ch.Delete()
}

// LoadHost gets the host definition from Object Storage
// logic: Read by ID; if error is ErrNotFound then read by name; if error is ErrNotFound return this error
//        In case of any other error, abort the retry to propagate the error
//        If retry times out, return errNotFound
func LoadHost(svc iaas.Service, ref string) (mh *Host, err error) {
	defer fail.OnPanic(&err)()

	if svc == nil {
		return nil, fail.InvalidParameterError("svc", "cannot be nil")
	}
	if ref == "" {
		return nil, fail.InvalidParameterError("ref", "cannot be empty string")
	}

	tracer := debug.NewTracer(nil, "("+ref+")", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogErrorWithLevel(tracer.TraceMessage(""), &err, logrus.TraceLevel)()

	// We first try looking for host by ID from metadata
	mh, err = NewHost(svc)
	if err != nil {
		return nil, err
	}

	retryErr := retry.WhileUnsuccessfulDelay1Second(
		func() error {
			innerErr := mh.ReadByReference(ref)
			if innerErr != nil {
				// If error is ErrNotFound, instructs retry to stop without delay
				if _, ok := innerErr.(fail.ErrNotFound); ok {
					return retry.AbortedError("no metadata found", innerErr)
				}

				if innerErr == stow.ErrNotFound { // FIXME: Remove stow dependency
					return retry.AbortedError("no metadata found", innerErr)
				}

				return innerErr
			}
			return nil
		},
		2*temporal.GetDefaultDelay(),
	)
	if retryErr != nil {
		switch realErr := retryErr.(type) {
		case retry.ErrAborted:
			return nil, realErr.Cause()
		case fail.ErrTimeout:
			return nil, realErr
		default:
			return nil, fail.Cause(realErr)
		}
	}

	ok, err := mh.OK()
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, fail.NotFoundError(fmt.Sprintf("reference %s not found", ref))
	}

	return mh, nil
}

// Acquire waits until the write lock is available, then locks the metadata
func (mh *Host) Acquire() {
	mh.item.Acquire()
}

// Release unlocks the metadata
func (mh *Host) Release() {
	mh.item.Release()
}
