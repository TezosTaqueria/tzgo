# Use the specified Golang base image
FROM golang:1.21.6-bullseye

VOLUME [ "/project" ]

# Set the working directory
WORKDIR /app

# Install Git (required to fetch Go packages)
RUN apt-get update && apt-get install -y git

# Copy the go.mod and go.sum files
COPY . /app/

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

# Build your application
RUN go build -o tzcompose /app/cmd/tzcompose

# Set working directory to Taqueria project
WORKDIR /project

# Command to run the application
CMD ["/app/tzcompose"]

ENTRYPOINT ["/app/tzcompose"]
