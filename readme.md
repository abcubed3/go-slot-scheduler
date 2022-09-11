# Go-Slot-Scheduler
A simple web utility to auto scheduler flex slot for an organization at certain hours of the day

curl -d '@data.json' http://127.0.0.1:8080/add_capacity -H "Content-Type:application/json"

## Deploy
```bash
$ gcloud run deploy go-slot-scheduler --region us-east1 --env-vars-file env.yaml --allow-unauthenticated --source .
```

## Development

```bash
$ gcloud builds submit --pack image=[IMAGE] us-east1` 
and 
$`gcloud run deploy go-slot-scheduler --image [IMAGE]`
```