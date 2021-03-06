/*
 * Copyright 2018-2020, CS Systemes d'Information, http://csgroup.eu
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
 *
 */

package outscale

import (
	"fmt"
	"reflect"

	"github.com/outscale-dev/osc-sdk-go/osc"

	"github.com/CS-SI/SafeScale/lib/utils/fail"
)

func normalizeErrorWithReason(reason string, err error) error {
	if err == nil {
		return nil
	}

	switch realErr := err.(type) {
	case osc.GenericOpenAPIError:
		switch model := realErr.Model().(type) {
		case osc.ErrorResponse:
			switch model.Errors[0].Code {
			case "1":
				return fail.UnauthorizedError("user is not authenticated")
			case "4045":
				return fail.InvalidRequestError("invalid CIDR")
			case "5057":
				return fail.NotFoundError("network not found")
			case "5071":
				return fail.NotFoundError("keypair not found")
			case "9011":
				return fail.DuplicateError("keypair already exists")
			case "9044":
				return fail.InvalidRequestError("not included in VPC CIDR")
			case "9058":
				return fail.DuplicateError("network already exist")
			default:
				merr := model.Errors[0]
				reqId := model.ResponseContext.RequestId
				return fail.UnknownError(
					fmt.Sprintf(
						"%s: from outscale driver, code='%s', type='%s', details='%s', requestId='%s'", reason, merr.Code,
						merr.Type, merr.Details, reqId,
					),
				)
			}
		default:
			return fail.UnknownError(
				fmt.Sprintf(
					"%s: from outscale driver, model='%s', error='%s'", reason, reflect.TypeOf(realErr.Model()), realErr.Error(),
				),
			)
		}
	default:
		return err
	}
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}

	switch realErr := err.(type) {
	case osc.GenericOpenAPIError:
		switch model := realErr.Model().(type) {
		case osc.ErrorResponse:
			switch model.Errors[0].Code {
			case "1":
				return fail.UnauthorizedError("user is not authenticated")
			case "4045":
				return fail.InvalidRequestError("invalid CIDR")
			case "5057":
				return fail.NotFoundError("network not found")
			case "5071":
				return fail.NotFoundError("keypair not found")
			case "9011":
				return fail.DuplicateError("keypair already exists")
			case "9044":
				return fail.InvalidRequestError("not included in VPC CIDR")
			case "9058":
				return fail.DuplicateError("network already exist")
			default:
				merr := model.Errors[0]
				reqId := model.ResponseContext.RequestId
				return fail.UnknownError(
					fmt.Sprintf(
						"from outscale driver, code='%s', type='%s', details='%s', requestId='%s'", merr.Code,
						merr.Type, merr.Details, reqId,
					),
				)
			}
		default:
			return fail.UnknownError(
				fmt.Sprintf(
					"from outscale driver, model='%s', error='%s'", reflect.TypeOf(realErr.Model()), realErr.Error(),
				),
			)
		}
	default:
		return err
	}
}
