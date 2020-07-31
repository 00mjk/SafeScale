/*
 * Copyright 2018-2020, CS Systemes d'Information, http://www.c-s.fr
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

package openstack

import (
    "reflect"
    "strings"

    "github.com/CS-SI/SafeScale/lib/utils/fail"
)

func caseInsensitiveContains(haystack, needle string) bool {
    lowerHaystack := strings.ToLower(haystack)
    lowerNeedle := strings.ToLower(needle)

    return strings.Contains(lowerHaystack, lowerNeedle)
}

func IsServiceUnavailableError(err error) bool {
    text := err.Error()

    return caseInsensitiveContains(text, "Service Unavailable")
}

func GetUnexpectedGophercloudErrorCode(err error) (int64, error) {
    xType := reflect.TypeOf(err)
    xValue := reflect.ValueOf(err)

    if xValue.Kind() != reflect.Struct {
        return 0, fail.NewError("not a gophercloud.ErrUnexpectedResponseCode")
    }

    _, there := xType.FieldByName("ErrUnexpectedResponseCode")
    if there {
        _, there := xType.FieldByName("Actual")
        if there {
            recoveredValue := xValue.FieldByName("Actual").Int()
            if recoveredValue != 0 {
                return recoveredValue, nil
            }
        }
    }

    return 0, fail.NewError("not a gophercloud.ErrUnexpectedResponseCode")
}
