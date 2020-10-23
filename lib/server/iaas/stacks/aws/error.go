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

package aws

import (
	"fmt"
	"github.com/CS-SI/SafeScale/lib/utils/debug/callstack"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/sirupsen/logrus"
	"reflect"
)

// normalizeError translates AWS error to SafeScale one
func normalizeError(err error) fail.Error {
	if err == nil {
		return nil
	}

	switch cerr := err.(type) {
	case awserr.Error:
		switch cerr.Code() {
		case "InvalidGroupId.Malformed":
			return fail.SyntaxError("failed to find Security Group: group id is malformed")
		case "InvalidGroup.NotFound":
			return fail.NotFoundError("failed to find Security Group")
		case "InvalidVpcID.NotFound":
			return fail.NotFoundError("failed to find vpc")
		case "InvalidGroup.Duplicate":
			return fail.DuplicateError("a security group already exists with that name")
		default:
			logrus.Debugf(callstack.DecorateWith("", "", fmt.Sprintf("Unhandled error (%s) received from provider: %s", reflect.TypeOf(err).String(), err.Error()), 0))
			return fail.NewError("unhandled error received from provider: %s", err.Error())
		}
	}

	return fail.ToError(err)
}