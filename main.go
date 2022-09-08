package main

import (
	// "bytes"
	"time"
	"context"
	// "flag"
	"errors"
	"fmt"
	"log"
	"os"
	"encoding/json"

	reservation "cloud.google.com/go/bigquery/reservation/apiv1"
	cloudtasks "cloud.google.com/go/cloudtasks/apiv2beta3"
	"google.golang.org/api/iterator"
	reservationpb "google.golang.org/genproto/googleapis/cloud/bigquery/reservation/v1"
	taskspb "google.golang.org/genproto/googleapis/cloud/tasks/v2beta3"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	adminProjectID  = "data-demo-269919"
	region          = "US"
	extraSlot       = int64(100)
	maxSlots        = int64(500)
	deleteCommitURL = "/del_capacity"
	queue           = "commit-delete-queue"
	queueRegion = "us-east1"
	minutes = 100
)

func main() {
	ctx := context.Background()
	commit, err := addCapacity(ctx, adminProjectID, region, extraSlot, maxSlots)
	if err != nil {
		log.Println(err)
	}

	if commit != nil {
		log.Printf("purchased commitmment, launching delete task for commit ID: %s", commit.Name)
		if err := launchDeleteTask(ctx, adminProjectID, queueRegion, queue, commit.Name, minutes); err != nil {
			log.Println(err)
		}
	}
}

func addCapacity(ctx context.Context, adminProjectID, region string, extraSlot, maxSlots int64) (*reservationpb.CapacityCommitment, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client, err := reservation.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	parentArg := fmt.Sprintf("projects/%s/locations/%s", adminProjectID, region)

	slotsToAdd := checkProjectSlots(ctx, client, parentArg, extraSlot, maxSlots)

	if slotsToAdd <= 0 {
		return nil, errors.New("slots to add to capacity can not be less than or equal to zero OR commitment has reached MAX Capacity Slot")
	}

	req := &reservationpb.CreateCapacityCommitmentRequest{
		// See https://pkg.go.dev/google.golang.org/genproto/googleapis/cloud/bigquery/reservation/v1#CreateCapacityCommitmentRequest.
		Parent: parentArg,
		CapacityCommitment: &reservationpb.CapacityCommitment{
			SlotCount: slotsToAdd,
			Plan:      reservationpb.CapacityCommitment_FLEX,
		},
	}
	resp, err := client.CreateCapacityCommitment(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func checkProjectSlots(ctx context.Context, client *reservation.Client, parent string, extraSlots, maxSlots int64) int64 {
	var total int64
	req := &reservationpb.ListCapacityCommitmentsRequest{
		// See https://pkg.go.dev/google.golang.org/genproto/googleapis/cloud/bigquery/reservation/v1#ListCapacityCommitmentsRequest.
		Parent: parent,
	}
	it := client.ListCapacityCommitments(ctx, req)
	for {
		resp, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Println(err)
			return 0
		}
		// TODO: Use resp.
		total = total + resp.SlotCount
	}

	slotCap := maxSlots - total
	return min(extraSlots, slotCap)
}

type Commit struct {
	CommitID string `json:"commit_id"`
}

func launchDeleteTask(ctx context.Context, adminProjectID, queueRegion, queue, commitName string, minutes int) error {
	host, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}

	deleteCommitURL = "https://" + host + deleteCommitURL
	log.Println(deleteCommitURL)

	c, err := cloudtasks.NewClient(ctx)
	if err != nil {
		return err
	}

	defer c.Close()

	body, err := json.Marshal(Commit{CommitID: commitName})
	if err != nil {
		return err
	}

	taskTime := time.Now().Add(time.Duration(minutes) * time.Minute)
	req := &taskspb.CreateTaskRequest{
		// TODO: Fill request struct fields.
		// See https://pkg.go.dev/google.golang.org/genproto/googleapis/cloud/tasks/v2beta3#CreateTaskRequest.
		Parent: fmt.Sprintf("projects/%s/locations/%s/queues/%s", adminProjectID, queueRegion, queue),
		Task: &taskspb.Task{
			PayloadType: &taskspb.Task_HttpRequest{
				HttpRequest: &taskspb.HttpRequest{
					Url:        deleteCommitURL,
					HttpMethod: taskspb.HttpMethod_POST,
					Body:       body,
					Headers: map[string]string{
						"Content-Type": "application/json",
					},
				},
			},
			ScheduleTime: timestamppb.New(taskTime),
		},
	}
	resp, err := c.CreateTask(ctx, req)
	if err != nil {
		return err
	}
	
	log.Printf("delete commitment task created %s", resp.Name)
	return nil
}

func deleteCapacity(ctx context.Context, commitName string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	client, err := reservation.NewClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	req := &reservationpb.DeleteCapacityCommitmentRequest{
		// See https://pkg.go.dev/google.golang.org/genproto/googleapis/cloud/bigquery/reservation/v1#DeleteCapacityCommitmentRequest.
		Name:commitName,
		Force: false,
	}

	err = client.DeleteCapacityCommitment(ctx, req)
	if err != nil {
		return err
	}

	log.Printf("capacity commitment %s deleted", commitName)
	return nil
}

func min(x, y int64) int64 {
	if x < y {
		return x
	}
	return y
}
