# Sample SALModule 1 

## Overview
This sample SAL Module implements a new `salmodule:Task` subclass named `GeoconnexReferenceFeatureStates` which fetches U.S. state reference features from the Geoconnex Reference Feature Server. 

## Assumptions

The following doc assume a unix command line environment.


## Usage

The following sections detail this SALModule's implementation of the SAL Module command line interface specification 

### Building the SAL Module

From this git repository's root directory run:

`docker build . -t sample-sal-module-1:latest` 

### Fetching the SAL Module's ontology

`docker run sample-sal-module-1:latest salmodule ontology`

### Running the SAL Modules Tasks

#### GeoconnexReferenceFeatureStates Task

`docker run -e SALMODULE_TASK_INSTANCE="$(cat SALMODULE_TASK_INSTANCE.sample)" sample-sal-module-1 salmodule run`

