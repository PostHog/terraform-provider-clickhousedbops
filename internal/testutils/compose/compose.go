package testutils

import (
	"fmt"
	"os"
	"os/exec"
)

type DockerComposeManager struct {
	projectPath string
}

func NewDockerComposeManager(projectPath string) *DockerComposeManager {
	return &DockerComposeManager{
		projectPath: projectPath,
	}
}

func (dcm *DockerComposeManager) Up(env map[string]string) error {
	args := []string{"up", "-d"}
	return dcm.runCommandWithEnv(env, args...)
}

func (dcm *DockerComposeManager) Down() error {
	return dcm.runCommand("down")
}

func (dcm *DockerComposeManager) runCommand(args ...string) error {
	return dcm.runCommandWithEnv(nil, args...)
}

func (dcm *DockerComposeManager) runCommandWithEnv(env map[string]string, args ...string) error {
	cmd := dcm.buildCommand(args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	environment := make([]string, 0)
	for k, v := range env {
		environment = append(environment, fmt.Sprintf("%s=%s", k, v))
	}

	cmd.Env = append(os.Environ(), environment...)

	return cmd.Run()
}

func (dcm *DockerComposeManager) buildCommand(args ...string) *exec.Cmd {
	// Try docker compose first (newer), then docker-compose (legacy)
	var cmd *exec.Cmd

	// Check if docker compose is available
	if dcm.isDockerComposeAvailable() {
		fullArgs := []string{"compose"}
		fullArgs = append(fullArgs, args...)
		cmd = exec.Command("docker", fullArgs...)
	} else {
		cmd = exec.Command("docker-compose", args...)
	}

	cmd.Dir = dcm.projectPath

	return cmd
}

func (dcm *DockerComposeManager) isDockerComposeAvailable() bool {
	cmd := exec.Command("docker", "compose", "version")
	err := cmd.Run()
	return err == nil
}
