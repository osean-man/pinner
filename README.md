# Pinner

A very simple tool to pin commands and store them into a local database. It's kind of like bash history, but 
organized with only things you put into the list. It uses a local sqlite3 db, so you have full control over your data.
I might make a way to sync dbs across users but right now I'm just living my best life and I'll add features as we go.

## Usage

```bash
Usage:
  pinner [flags]
  pinner [command]

Available Commands:
  add         Add a new pinned command
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  list        List all pinned commands
  remove      Remove a pinned command
  update      Update a pinned command

Flags:
  -c, --copy   Copy the command to your clipboard instead of executing it
  -h, --help   help for pinner


```

### Examples
```bash
# Access and execute a pinned command
pinner

# Copy a command to your clipboard
pinner -c 

# Add a new pinned command
pinner add "ls -la"

# Echo a command into the list
echo "ls -la" | pinner add

# List all pinned commands
pinner list

# Update a command
pinner update 

# Remove a pinned command
pinner remove 
```

### SECURITY IS NOT A PRIORITY HERE
Keep in mind this is just a little utility I made for myself. It's definitely not secure and upon selecting a command it
will execute it. You should not use this if you are in an env with more than yourself. Even then, don't use it. Yet. :)

I've also noticed that there are some issues if you save a command, and then you want to execute that and tee it into a 
file. The first line will actually be the command you ran. I don't have the will to fix it because it's not
an issue for me. :)


## Installation
The simple way:
```bash
git clone git@github.com:osean-man/pinner.git
cd pinner 
go build -o pinner main.go 
```

Then you can move the binary to a location in your path if you want it available everywhere. For example:
```bash
chmod +x pinner
sudo mv pinner /usr/local/bin
```

Depending on your system and build target you can also flag the build command with the target OS and architecture.
This article explains some of it: https://www.digitalocean.com/community/tutorials/how-to-build-go-executables-for-multiple-platforms-on-ubuntu-16-04
i.e.
```bash
GOOS=windows GOARCH=amd64 go build -o pinner.exe main.go
```

### NOTES
I have not tested this, there are no tests and I only run it on Linux so, I have no idea if it works on Windows or Mac.

## License
[GOOD LUCK WITH THAT SHIT PUBLIC LICENSE](https://github.com/osean-man/pinner/blob/master/LICENSE)
