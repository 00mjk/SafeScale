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

package listeners

import (
	"context"
	"fmt"

	"github.com/CS-SI/SafeScale/lib/utils/debug"

	googleprotobuf "github.com/golang/protobuf/ptypes/empty"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/CS-SI/SafeScale/lib"
	"github.com/CS-SI/SafeScale/lib/server/handlers"
	srvutils "github.com/CS-SI/SafeScale/lib/server/utils"
	"github.com/CS-SI/SafeScale/lib/utils/fail"
)

// JobManagerHandler ...
var JobManagerHandler = handlers.NewJobHandler

// JobManagerListener service server gRPC
type JobManagerListener struct{}

// Stop specified process
func (s *JobManagerListener) Stop(ctx context.Context, in *pb.JobDefinition) (empty *googleprotobuf.Empty, err error) {
	empty = &googleprotobuf.Empty{}
	if s == nil {
		return empty, status.Errorf(codes.FailedPrecondition, fail.InvalidInstanceError().Message())
	}
	if in == nil {
		return empty, status.Errorf(codes.InvalidArgument, fail.InvalidParameterError("in", "cannot be nil").Message())
	}
	uuid := in.Uuid
	if in.Uuid == "" {
		return empty, status.Errorf(codes.FailedPrecondition, "Can't stop job: job id not set")
	}

	tracer := debug.NewTracer(nil, fmt.Sprintf("('%s')", uuid), true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &err)()

	log.Infof("Received stop order for job '%s'...", uuid)

	ctx, cancelFunc := context.WithCancel(ctx)
	if err := srvutils.JobRegister(ctx, cancelFunc, "Stop job "+uuid); err == nil {
		defer srvutils.JobDeregister(ctx)
	}

	tenant := GetCurrentTenant()
	if tenant == nil {
		log.Info("Can't stop process: no tenant set")
		return empty, status.Errorf(codes.FailedPrecondition, "Can't stop process: no tenant set")
	}

	handler := JobManagerHandler(tenant.Service)
	handler.Stop(ctx, in.Uuid)

	return empty, nil
}

// List running process
func (s *JobManagerListener) List(ctx context.Context, in *googleprotobuf.Empty) (jl *pb.JobList, err error) {
	if s == nil {
		return nil, status.Errorf(codes.FailedPrecondition, fail.InvalidInstanceError().Message())
	}

	tracer := debug.NewTracer(nil, "", true).GoingIn()
	defer tracer.OnExitTrace()()
	defer fail.OnExitLogError(tracer.TraceMessage(""), &err)()

	ctx, cancelFunc := context.WithCancel(ctx)

	if err := srvutils.JobRegister(ctx, cancelFunc, "List Processes"); err == nil {
		defer srvutils.JobDeregister(ctx)
	}

	tenant := GetCurrentTenant()
	if tenant == nil {
		log.Info("Can't list process : no tenant set")
		return nil, status.Errorf(codes.FailedPrecondition, "Can't list process: no tenant set")
	}

	handler := JobManagerHandler(tenant.Service)
	processMap, err := handler.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list Process %s", getUserMessage(err))
	}
	var pbProcessList []*pb.JobDefinition
	for uuid, info := range processMap {
		pbProcessList = append(pbProcessList, &pb.JobDefinition{Uuid: uuid, Info: info})
	}

	return &pb.JobList{List: pbProcessList}, nil
}
