# mbaigo System: KGrapher

## Purpose
This system offers as a service the semantic model of its local cloud.
It obtains the list of systems from the Service Registrar and then asks each system for its model.
It can then aggregate them to represent the complete local cloud, a distributed system of systems, each with the assets it has and the services that are provided and consumed.

The asset of the system is GraphDB, a graph database that systematically collects data along with the relationships between the different data entities.
Currently, when the KGrapher’s only service is invoked from a web browser, it generates the knowledge graph of the local cloud, pushes it to the database and provides a text version to the web browser.

Using the model in conjunction with the Arrowhead Framework Ontology (afo), a computer can infer new knowledge about the local cloud and reason about it.

The KGrapher also serves (makes available) local ontologies and knowledge graphs.
One can obtain these description through the *localOntologies* service.

## Compiling
To compile the code, one needs initialize the *go.mod* file with ``` go mod init github.com/sdoque/systems/kgrapher``` before running *go mod tidy*.

To run the code, one just needs to type in ```go run .``` within a terminal or at a command prompt.

It is **important** to start the program from within its own directory (and each system should have their own directory) because it looks for its configuration file there. If it does not find it there, it will generate one and shutdown to allow the configuration file to be updated.

The configuration and operation of the system can be verified using the system's web server using a standard web browser, whose address is provided by the system at startup.

To build the software for one's own machine,
```go build -o modeler_imac```, where the ending is used to clarify for which platform the code is for.


## Cross compiling/building
The following commands enable one to build for different platforms:

- Raspberry Pi 64: ```GOOS=linux GOARCH=arm64 go build -o kgrapher_rpi64```

One can find a complete list of platform by typing *‌go tool dist list* at the command prompt

If one wants to secure copy it to a Raspberry pi,
`scp kgrapher_rpi64 jan@192.168.1.195:demo/kgrapher/` where user is the *username* @ the *IP address* of the Raspberry Pi with a relative (to the user's home directory) target *demo/kgrapher/* directory.

## Deploying the asset
An easy way to deploy Ontotext GraphDB is with the use go Docker.

### 1. **Installing Docker**

#### Command:
```bash
curl -sSL https://get.docker.com | sh
```
- **`curl -sSL`**: This command uses `curl`, a tool to transfer data from or to a server. Here, it fetches the installation script for Docker.
  - `-s` means "silent mode," so it won't show progress bars.
  - `-S` makes sure to show errors if they occur.
  - `-L` follows any redirects the URL might have.
  
- **`https://get.docker.com`**: This is the URL of Docker’s official installation script, hosted by Docker.
  
- **`| sh`**: This takes the output of the `curl` command (the script downloaded from Docker's server) and immediately runs it using `sh` (the shell command interpreter).
  
In essence, this command fetches and runs Docker’s installation script, automatically installing Docker onto your Raspberry Pi. It ensures you're getting the most recent version of Docker.

#### Security note:
This command is convenient, but it's generally a good practice to inspect scripts you download from the internet before running them. You can review it by downloading the script first without piping it into `sh`:
```bash
curl -sSL https://get.docker.com -o get-docker.sh
```
Then inspect the file with:
```bash
cat get-docker.sh
```
And run it manually with:
```bash
sh get-docker.sh
```

### 2. **Adding the User to the Docker Group**

#### Command:
```bash
sudo usermod -aG docker pi
```

- **`sudo`**: This command gives you superuser privileges, allowing you to execute actions that require administrative rights.
  
- **`usermod -aG docker pi`**:
  - **`usermod`**: This modifies a user’s account settings.
  - **`-aG docker`**: This option appends the user to a group. In this case, it's appending the user to the `docker` group.
    - `-a`: Append (don’t remove the user from other groups).
    - `-G`: Specifies the group to which the user will be added (in this case, the `docker` group).
  - **`pi`**: This is the username of the Raspberry Pi user account (if your account is named something different, replace `pi` with your username).

By adding the user to the `docker` group, you're giving the `pi` user permission to run Docker commands **without needing to use `sudo`** every time. After this, you'll need to log out and log back in (or reboot) for the changes to take effect.

### 3. **Pulling the GraphDB Docker Image**

#### Command:
```bash
docker pull ontotext/graphdb
```

- **`docker pull`**: This tells Docker to download (pull) an image from a Docker registry (by default, Docker Hub).
  
- **`ontotext/graphdb`**: This is the name of the Docker image you're pulling. It's hosted on Docker Hub under the `ontotext` organization, and the specific image is `graphdb`.

This command downloads the `graphdb` Docker image to your Raspberry Pi, allowing you to run a container based on it. Docker images contain everything needed to run an application, including the application code, system libraries, and dependencies.

### 4. **Running the GraphDB Docker Container**

#### Command:
```bash
docker run -d -p 7200:7200 --name graphdb -t ontotext/graphdb:10.7.4
docker run -d --network host --name graphdb ontotext/graphdb:10.7.4
```

- **`docker run`**: This starts a new container from a Docker image.
  
- **`-d`**: This tells Docker to run the container in **detached mode**, meaning the container will run in the background and won't tie up your terminal session.
  
- **`-p 7200:7200`**: This option maps port 7200 on your Raspberry Pi (host) to port 7200 inside the Docker container. GraphDB uses port 7200 for its web interface, so this allows you to access GraphDB on your Raspberry Pi through `http://<your_raspberry_pi_ip>:7200`.

- **`--name graphdb`**: This gives the container a name (`graphdb`). This is useful for managing or referring to the container later (e.g., stopping it, viewing logs, etc.).

- **`ontotext/graphdb`**: This specifies the image to use to create the container. Docker will use the image you pulled earlier.


To shut the running instance, type ```docker stop graphdb``` and then ```docker rm graphdb``` .