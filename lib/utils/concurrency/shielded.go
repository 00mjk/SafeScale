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

package concurrency

import (
	"github.com/CS-SI/SafeScale/lib/utils/data"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
)

// Shielded allows to store data with controlled access to it
type Shielded struct {
	witness data.Clonable
	lock    TaskedLock
}

// NewShielded creates a new protected data
func NewShielded(witness data.Clonable) *Shielded {
	return &Shielded{
		witness: witness,
		lock:    NewTaskedLock(),
	}
}

// Clone ...
func (d *Shielded) Clone() *Shielded {
	return NewShielded(d.witness.Clone())
}

// // LockShared is used to lock a clonable for read
// // Returns a Protector, on which can be applied method 'Shield()'
// func (d *Shielded) LockShared(task Task) (Protector, error) {
// 	if d == nil {
// 		return nil, fail.InvalidInstanceError()
// 	}
// 	if task == nil {
// 		return nil, fail.InvalidParameterError("task", "cannot be nil")
// 	}
// 	if d.witness == nil {
// 		return nil, fail.InvalidParameterError("d.witness", "cannot be nil; use concurency.NewShielded() to instanciate")
// 	}
// 	d.lock.RLock(task)
// 	return &protector{shielded: d, readLock: true}, nil
// }

// Inspect is used to lock a clonable for read
func (d *Shielded) Inspect(task Task, inspector func(clonable data.Clonable) error) error {
	if d == nil {
		return fail.InvalidInstanceError()
	}
	if task == nil {
		return fail.InvalidParameterError("task", "cannot be nil")
	}
	if inspector == nil {
		return fail.InvalidParameterError("inspector", "cannot be nil")
	}
	if d.witness == nil {
		return fail.InvalidParameterError("d.witness", "cannot be nil; use concurency.NewShielded() to instanciate")
	}
	d.lock.RLock(task)
	defer d.lock.RUnlock(task)

	return inspector(d.witness.Clone())
}

// // LockExclusive is used to lock a clonable for write
// // Returns a Protector, on which can be applied methods 'ShieldXXX()'
// func (d *Shielded) LockExclusive(task Task) (Protector, error) {
// 	if d == nil {
// 		return nil, fail.InvalidInstanceError()
// 	}
// 	if task == nil {
// 		return nil, fail.InvalidParameterError("task", "cannot be nil")
// 	}
// 	if d.witness == nil {
// 		return nil, fail.InvalidParameterError("d.witness", "cannot be nil; use concurency.NewData() to instanciate")
// 	}

// 	d.lock.Lock(task)
// 	return &protector{shielded: d, readLock: false}, nil
// }

// Alter allows to update a clonable using a write lock
func (d *Shielded) Alter(task Task, alterer func(data.Clonable) error) error {
	if d == nil {
		return fail.InvalidInstanceError()
	}
	if task == nil {
		return fail.InvalidParameterError("task", "cannot be nil")
	}
	if alterer == nil {
		return fail.InvalidParameterError("alterer", "cannot be nil")
	}
	if d.witness == nil {
		return fail.InvalidParameterError("d.witness", "cannot be nil; use concurency.NewData() to instanciate")
	}

	d.lock.Lock(task)
	defer d.lock.Unlock(task)

	clone := d.witness.Clone()
	err := alterer(clone)
	if err != nil {
		return err
	}
	_ = d.witness.Replace(clone)
	return nil
}
