# Go-Slot-Scheduler
A simple web utility service to auto schedule BigQuery flex slots for an organization at certain hours of the day, in a serverless mode with Cloud Run, Tasks and Scheduler.

## Deploy
* Clone repo in Cloud Shell of project assigned for BigQuery Reservation and change directory to go-slot-scheduler/
* Set GCloud parameters if not set, choose region of compute
```bash
export PROJECT_ID=$(gcloud config get-value project)
gcloud config set compute/region us-east1
```

### Create a Task Queue
``` bash
QUEUE_ID=commit-delete-queue
QUEUE_LOCATION=us-east1
gcloud tasks queues create $QUEUE_ID --location=$QUEUE_LOCATION
```
# TODO: Add terraform example

### Create or Grant service account with Bigquery resource admin permission
* Use default compute service account for Cloud Run
``` bash
export PROJECT_ID=$(gcloud config get-value project)
SERV_ACCT=`gcloud iam service-accounts list --format="value(email)" | grep compute@developer.gserviceaccount.com`
gcloud projects add-iam-policy-binding $PROJECT_ID \
--member="serviceAccount:${SERV_ACCT}" \
--role="roles/bigquery.resourceAdmin" \
--condition=None
```
OR
* (Recommended) For additional [security or least privilege](https://cloud.google.com/run/docs/securing/service-identity#per-service-identity), create a custom service account and grant `roles/bigquery.resourceAdmin`, `roles/cloudtasks.admin` and `roles/run.admin`
# TODO: Scope this to the minimal policy grants vs. multiple admin roles

``` bash
gcloud iam service-accounts create slot-scheduler-sa \
    --description="go-slot-scheduler service account" \
    --display-name="Slot Scheduler"
SERV_ACCT=slot-scheduler-sa@$PROJECT_ID.iam.gserviceaccount.com

gcloud iam service-accounts add-iam-policy-binding \
  $(gcloud projects describe $PROJECT_ID --format="value(projectNumber)")-compute@developer.gserviceaccount.com \
  --member="serviceAccount:${SERV_ACCT}" \
  --role="roles/iam.serviceAccountUser" \
  --condition=None

gcloud projects add-iam-policy-binding $PROJECT_ID \
--member="serviceAccount:${SERV_ACCT}" \
--role="roles/bigquery.resourceAdmin" \
--condition=None

gcloud projects add-iam-policy-binding $PROJECT_ID \
--member="serviceAccount:${SERV_ACCT}" \
--role="roles/run.admin" \
--condition=None

gcloud projects add-iam-policy-binding $PROJECT_ID \
--member="serviceAccount:${SERV_ACCT}" \
--role="roles/cloudtasks.admin" \
--condition=None

```
# TODO: Terraform policy creation and binding

### Deploy to CloudRun
* Ensure you have the right permissions to deploy to [CloudRun](https://cloud.google.com/run/docs/deploying#permissions_required_to_deploy), using [Cloud Build from source](https://cloud.google.com/run/docs/deploying-source-code#permissions_required_to_deploy) and [Artifact Registry](https://cloud.google.com/artifact-registry/docs/access-control#roles)
* Update environment variables for MAX_SLOTS for the organization or slot commitment
* Deploy service to Cloudrun 
```bash
REGION=$(gcloud config get-value compute/region)
MAX_SLOTS=500

gcloud run deploy go-slot-scheduler --region ${REGION} --set-env-vars=MAX_SLOTS=${MAX_SLOTS},QUEUE_ID=${QUEUE_ID},QUEUE_LOCATION=${QUEUE_LOCATION} --no-allow-unauthenticated --service-account=$SERV_ACCT --source .
```
# TODO: Terraform cloud run service enablement with app stack separate, I would consider moving the above to a service.yaml definition that can be modified and then used wiht gcloud run service ...

* Payload of http request in `data.json`
``` json
{
    "extra_slot":100,
    "region":"us",
    "minutes": 180
}
```
# Question: how do project quotas play here? ideally, this should check the project quota in the region before validating if the slots can be purchased.
# Question: We add the slot commitment, IIRC we need to change the reservation(s) to take advantage of the larger slot capacity. Otherwise the purchase will not be leveraged.

```bash
# Get the Cloudrun service https endpoint
ENDPOINT=$(gcloud run services describe go-slot-scheduler --region $REGION --format 'value(status.url)')

# Call the service with sample data 
# Permission will be denied because it is an internal service. If you want to test use `--allow-unauthenticated` in cloud run deploy command
curl -d '@data.json' $ENDPOINT/add_capacity -H "Content-Type:application/json"
```
# TODO: you should be able to use this by getting an identity token from the cloud run service

### Set up schedule with Cloud Scheduler
``` bash
# Schedule 100 extra slots at 6AM M-F, for 10 hours
# https://cloud.google.com/sdk/gcloud/reference/scheduler/jobs/create/http 
# Default timezone is UTC, but you can change with an extra command `--time-zone="est"`
gcloud scheduler jobs create http slot-schedule \
    --location=$QUEUE_LOCATION \
    --schedule="* 6 * * 1-5" \
    --headers="Content-Type=application/json" \
    --uri="${ENDPOINT}/add_capacity" \
    --message-body-from-file=data.json \
    --oidc-service-account-email=${SERV_ACCT}
```

## Development

```bash
gcloud builds submit --pack image=[IMAGE] us-east1 
and 
gcloud run deploy go-slot-scheduler --image [IMAGE]

OR run on Docker locally
```

## Future Work
* Adjust dedicated slot assignments for projects after capacity adjustment
* Add schedule frequency to environment and create job schedules 
* Add monitoring / assignment capability
* Add check for max project / region quotas

## Credit
Go-slot-scheduler is inspired by [bq-slot-scheduler](https://github.com/pdunn/bq-slot-scheduler) in python written by [Patrick Dunn](https://medium.com/google-cloud/scheduling-bigquery-slots-2a2beba42711) for AppEngine