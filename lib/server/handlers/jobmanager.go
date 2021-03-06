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

package handlers

import (
	"context"

	"github.com/CS-SI/SafeScale/lib/server/iaas"
	srvutils "github.com/CS-SI/SafeScale/lib/server/utils"
)

//go:generate mockgen -destination=../mocks/mock_JobManager.go -package=mocks github.com/CS-SI/SafeScale/lib/server/handlers JobManagerAPI

// JobManagerAPI defines API to manipulate process
type JobManagerAPI interface {
	List(ctx context.Context) (map[string]string, error)
	Stop(ctx context.Context, uuid string)
}

// JobManagerHandler service
type JobManagerHandler struct {
	service iaas.Service
}

// NewJobHandler creates a Volume service
func NewJobHandler(svc iaas.Service) JobManagerAPI {
	return &JobManagerHandler{
		service: svc,
	}
}

// List returns the Running Process list
func (pmh *JobManagerHandler) List(ctx context.Context) (map[string]string, error) {
	return srvutils.JobList(), nil
}

// Stop stop the designed Process
func (pmh *JobManagerHandler) Stop(ctx context.Context, uuid string) {
	srvutils.JobCancelUUID(uuid)
}
