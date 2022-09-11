package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	reservation "cloud.google.com/go/bigquery/reservation/apiv1"
	cloudtasks "cloud.google.com/go/cloudtasks/apiv2beta3"
	"cloud.google.com/go/compute/metadata"
	"github.com/gorilla/mux"
	"google.golang.org/api/iterator"
	reservationpb "google.golang.org/genproto/googleapis/cloud/bigquery/reservation/v1"
	taskspb "google.golang.org/genproto/googleapis/cloud/tasks/v2beta3"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	addCapacityPath    = "/add_capacity"
	deleteCapacityPath = "/del_capacity"

	defaultRegion      = "US"
	defaultMinute      = int64(1)

)

var (
	maxSlots           int64
	queue, queueLocation string
	port, projectID string
)

// ENV config
type Config struct {
	MaxSlot     int64
	QueueID     string
	QueueLocation string
}

func init() {
	var err error
	// Run from BigQuery Admin project
	if projectID = os.Getenv("GOOGLE_CLOUD_PROJECT"); projectID == "" {
		projectID, err = metadata.ProjectID()
		if err != nil {
			log.Fatalf("projectID is not provided")
		}
	}

	if port = os.Getenv("PORT"); port == "" {
		port = "8080"
	}

	if maxSlots, err = strconv.ParseInt(os.Getenv("MAX_SLOTS"), 10, 64); err != nil {
		log.Fatal("error: cannot parse MAX_SLOTS")
	} else if maxSlots <= 0 {
		log.Fatal("MAX_SLOTS can not be less than or equal to zero.")
	}

	if queue = os.Getenv("QUEUE_ID"); queue == "" {
		log.Fatal("QUEUE_ID can not be empty. Create and provide a queue id")
	}

	if queueLocation = os.Getenv("QUEUE_LOCATION"); queueLocation == "" {
		log.Fatal("QUEUE_REGION can not be empty. Provide queue region")
	}
}

func main() {
	r := mux.NewRouter()
	r.HandleFunc(addCapacityPath, addCapacityHandler).Methods("POST")
	r.HandleFunc(deleteCapacityPath, deleteCapacityHandler).Methods("POST")
	r.HandleFunc("/healthz", healthzHandler).Methods("GET")

	srv := &http.Server{
		Handler: r,
		Addr:    ":" + port,

		WriteTimeout: 60 * time.Second,
		ReadTimeout:  30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("starting server on port %s", port)
		if err := srv.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	srv.Shutdown(ctx)

	log.Println("shutting down")
	os.Exit(0)
}

// HTTP request payload for adding capacity
type Payload struct {
	Minutes   int64  `json:"minutes"`
	Region    string `json:"region"`
	ExtraSlot int64  `json:"extra_slot"`
}

func addCapacityHandler(w http.ResponseWriter, r *http.Request) {
	var p Payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "errors: %v", err)
		return
	}
	defer r.Body.Close()

	if p.Region == "" {
		p.Region = defaultRegion
	}
	if p.Minutes <= 0 {
		p.Minutes = defaultMinute
	}
	if p.ExtraSlot == 0 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "errors: required extraslot not provided")
		return
	}
	log.Printf("request to add capacity: %s", p)
	
	commit, err := addCapacity(r.Context(), projectID, p.Region, p.ExtraSlot, maxSlots)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "errors: %v", err)

		log.Println(err)
		return
	}

	if commit != nil {
		log.Printf("purchased commitmment, launching delete task for commit ID: %s", commit.Name)
		if err := launchDeleteTask(r.Context(), r, projectID, queueLocation, queue, commit.Name, p.Minutes); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "errors: %v", err)

			log.Println(err)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"data":"request processed"}"`))
	w.Write([]byte("\n"))
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":ok}`)
	fmt.Fprintf(w, "\n")
}

func addCapacity(ctx context.Context, adminProjectID, region string, extraSlot, maxSlots int64) (*reservationpb.CapacityCommitment, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	client, err := reservation.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	parent := fmt.Sprintf("projects/%s/locations/%s", adminProjectID, region)

	slotsToAdd, err := checkProjectSlots(ctx, client, parent, extraSlot, maxSlots)
	if err != nil {
		return nil, fmt.Errorf("getting project slots: %v", err)
	}
	if slotsToAdd <= 0 {
		return nil, errors.New("slots to add to capacity can not be less than or equal to zero OR commitment has reached MAX Capacity Slot")
	}

	req := &reservationpb.CreateCapacityCommitmentRequest{
		// See https://pkg.go.dev/google.golang.org/genproto/googleapis/cloud/bigquery/reservation/v1#CreateCapacityCommitmentRequest.
		Parent: parent,
		CapacityCommitment: &reservationpb.CapacityCommitment{
			SlotCount: slotsToAdd,
			Plan:      reservationpb.CapacityCommitment_FLEX,
		},
	}
	resp, err := client.CreateCapacityCommitment(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("creating capacity commitment: %v", err)
	}

	return resp, nil
}

func checkProjectSlots(ctx context.Context, client *reservation.Client, parent string, extraSlots, maxSlots int64) (int64, error) {
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
			return 0, err
		}
		total = total + resp.SlotCount
	}

	slotCap := maxSlots - total
	return min(extraSlots, slotCap), nil
}

// Commit request for deleteCapacity
type Commit struct {
	CommitID string `json:"commit_id"`
}

func launchDeleteTask(ctx context.Context, r *http.Request, adminProjectID, queueRegion, queue, commitName string, minutes int64) error {
	host := r.Host

	deleteURL := "https://" + host + deleteCapacityPath

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
		// See https://pkg.go.dev/google.golang.org/genproto/googleapis/cloud/tasks/v2beta3#CreateTaskRequest.
		Parent: fmt.Sprintf("projects/%s/locations/%s/queues/%s", adminProjectID, queueRegion, queue),
		Task: &taskspb.Task{
			PayloadType: &taskspb.Task_HttpRequest{
				HttpRequest: &taskspb.HttpRequest{
					Url:        deleteURL,
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


func deleteCapacityHandler(w http.ResponseWriter, r *http.Request) {
	var c Commit
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "errors: %v", err)
		return
	}
	defer r.Body.Close()

	if c.CommitID == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "errors: required CommitID not provided")
		return
	}

	if err := deleteCapacity(r.Context(), c.CommitID); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "errors: %v", err)

		log.Println(err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"data":"request processed"}"`))
	w.Write([]byte("\n"))
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
		Name:  commitName,
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
