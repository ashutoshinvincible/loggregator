package dea_logging_agent

import (
	"encoding/json"
)

func ReadInstances(data []byte) ([]Instance, error) {
	type instanceJson struct {
		Application_id string
		Warden_job_id uint64
		Warden_container_path string
	}

	type instancesJson struct {
		Instances []instanceJson
	}

	var instances []Instance
	var jsonInstances instancesJson
	error := json.Unmarshal(data, &jsonInstances)

	instances = make([]Instance, len(jsonInstances.Instances))
	for i, jsonInstance := range jsonInstances.Instances {
		instances[i] = Instance{
			ApplicationId: jsonInstance.Application_id,
			WardenJobId: jsonInstance.Warden_job_id,
			WardenContainerPath: jsonInstance.Warden_container_path}
	}

	return instances, error
}
