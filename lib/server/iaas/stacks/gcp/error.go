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

package gcp

import (
	"github.com/CS-SI/SafeScale/lib/utils/fail"
	"google.golang.org/api/googleapi"
)

// normalizeError translates AWS error to SafeScale one
func normalizeError(err error) fail.Error {
	if err == nil {
		return nil
	}

	switch cerr := err.(type) {
	case *googleapi.Error:

		switch cerr.Code {
		case 404:
			return fail.NotFoundError(err.Error())
		}
	}

	return fail.ToError(err)
}