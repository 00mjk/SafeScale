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
	"reflect"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/sirupsen/logrus"

	"github.com/CS-SI/SafeScale/lib/utils/debug/callstack"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
)

func normalizeErrorWithMsg(err error, msg ...string) fail.Error {
	if err == nil {
		return nil
	}

	if len(msg) == 0 {
		return normalizeError(err)
	}

	if _, ok := err.(awserr.Error); ok {
		if reqErr, ok := err.(awserr.RequestFailure); ok {
			code := reqErr.StatusCode()
			switch code {
			case 400:
				return fail.InvalidRequestError(msg, err.Error())
			case 401:
				return fail.NotAuthenticatedError(msg, err.Error())
			case 403:
				return fail.ForbiddenError(msg, err.Error())
			case 404:
				return fail.NotFoundError(msg, err.Error())
			case 408:
				return fail.TimeoutError(err, 0, msg, err.Error())
			case 409:
				return fail.InvalidRequestError(msg, err.Error())
			case 410:
				return fail.NotFoundError(msg, err.Error())
			case 425:
				return fail.OverloadError(msg, err.Error())
			case 429:
				return fail.OverloadError(msg, err.Error())
			case 500:
				return fail.ExecutionError(nil, msg, err.Error())
			case 503:
				return fail.NotAvailableError(msg, err.Error())
			case 504:
				return fail.NotAvailableError(msg, err.Error())
			default:
				logrus.Warnf(
					callstack.DecorateWith(
						"", "", fmt.Sprintf(
							"Unhandled error (%s) received from provider: %s", reflect.TypeOf(err).String(),
							err.Error(),
						), 0,
					),
				)
				return fail.NewError(msg, "unhandled error received from provider: %s", err.Error())
			}
		}
	}

	return fail.ToError(err)
}

// normalizeError translates AWS error to SafeScale one
func normalizeError(err error) fail.Error {
	if err == nil {
		return nil
	}

	if _, ok := err.(awserr.Error); ok {
		if reqErr, ok := err.(awserr.RequestFailure); ok {
			code := reqErr.StatusCode()
			switch code {
			case 400:
				return fail.InvalidRequestError(err.Error())
			case 401:
				return fail.NotAuthenticatedError(err.Error())
			case 403:
				return fail.ForbiddenError(err.Error())
			case 404:
				return fail.NotFoundError(err.Error())
			case 408:
				return fail.TimeoutError(err, 0)
			case 409:
				return fail.InvalidRequestError(err.Error())
			case 410:
				return fail.NotFoundError(err.Error())
			case 425:
				return fail.OverloadError(err.Error())
			case 429:
				return fail.OverloadError(err.Error())
			case 500:
				return fail.ExecutionError(nil, err.Error())
			case 503:
				return fail.NotAvailableError(err.Error())
			case 504:
				return fail.NotAvailableError(err.Error())
			default:
				logrus.Warnf(
					callstack.DecorateWith(
						"", "", fmt.Sprintf(
							"Unhandled error (%s) received from provider: %s", reflect.TypeOf(err).String(), err.Error(),
						), 0,
					),
				)
				return fail.NewError("unhandled error received from provider: %s", err.Error())
			}
		}
	}

	return fail.ToError(err)
}
