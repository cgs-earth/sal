import click
import requests
import json
import sys
import os

# The module's vocabulary lives in ontology.jsonld next to this script rather
# than in code, so the same document can be read, edited, and validated on its
# own. The Dockerfile copies it into the image beside main.py.
ONTOLOGY_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "ontology.jsonld")

# 1. Top-level main CLI group
@click.group()
def cli():
    """Main application CLI."""
    pass

# 2. 'salmodule' group nested under the main CLI
@cli.group("salmodule")
def salmodule():
    """Implements the salmodule (CLI) specification"""
    pass

# 3. 'ontology' subcommand nested under 'salmodule'
@salmodule.command("ontology")
def ontology():
    """Print this module's ontology, read from the ontology.jsonld that sits beside this script."""
    with open(ONTOLOGY_PATH) as f:
        onto = json.load(f)
    print(json.dumps(onto, indent=4))


@salmodule.command("run")
def run():
    """Execute the salmodule:Task subclass instance set in SALMODULE_TASK_INSTANCE env"""
    task_instance = get_salmodule_task_instance()
    salmodule_task_2_handler(task_instance['@type'])(task_instance)


def geoconnex_reference_feature_states(task_instance):
    """salmodule:Task implementation"""
 
    # get maxRetries property value as per the salmodule:taskShape NodeShape definition for this task instance
    max_retries=0
    if isinstance(task_instance['maxRetries'],dict):
        max_retries=int(task_instance['maxRetries']['@value']) # datatype rendered as a object. Get the @value 
    else:
        max_retries = int(task_instance['maxRetries']) # datatype is a literal 

    states_items_url = "https://reference.geoconnex.us/collections/states/items"

    for i in range(max_retries):
        try:
            r = requests.get(states_items_url,headers={"Accept": "application/ld+json"})
            if r.status_code != 200:
                print_err_msg(f"Failed to fetch data: status code = {r.status_code}, reason = {r.reason}")
                sys.exit(1)
            docs = json.loads(r.text)
            for feature in docs['features']:
                print_states_jsonld(feature['@id'],max_retries)
            break
        except requests.RequestException as e:
            if i == max_retries - 1:
                print_err_msg(f"Failed to connect reference features server 'states' items URL after {max_retries} attempts.")
                sys.exit(1)
                      
def print_states_jsonld(state_pid,max_retries):
    """Fetch state from reference feature server identified by state_pid and print to stdout"""
    for i in range(max_retries):
        try:
            r = requests.get(state_pid, headers={"Accept": "application/ld+json"})
            if r.status_code != 200:
                print_err_msg(f"Failed to fetch state data: status code = {r.status_code}, reason = {r.reason}")
                sys.exit(1)
            doc = json.loads(r.text)
            del doc['@context'] # delete context. should output only JSON. SALModule caller will inject this SAL Module's @context to convert terms to triples 
            print(json.dumps(doc))
            break
        except requests.RequestException as e:
            print_err_msg(f"Request failed: {e}")
            if i == max_retries - 1:
                print_err_msg(f"Failed to fetch state data after {max_retries} attempts.")
                sys.exit(1)
            else:
                continue

def print_err_msg(msg):
    """Print an error message as a JSON object to stdout in conformance to salmodule:stdoutShape SHACL Shape annotation for salmodule:Task base class"""
    err_msg = {
        "@type": "salmodule:Error",
        "rdfs:comment": msg

    }
    print( json.dumps(err_msg))

def get_salmodule_task_instance():
    """Retrieve the SAL Module task instance from the environment variable SALMODULE_TASK_INSTANCE and return it as a dictionary."""
    task_inst = os.environ.get("SALMODULE_TASK_INSTANCE")
    if not task_inst:
        print_err_msg("SALMODULE_TASK_INSTANCE environment variable is not set.")
        sys.exit(1)

    task_inst_dict = json.loads(task_inst)

    return task_inst_dict

def salmodule_task_2_handler(task_name):
    """resolve task name (salmodule:Task subclass) to corresponding handler function."""

    #  Below assumes that SAL abides by @context terms as set forth in a SAL Module's ontology
    #  In the ontology definition (see ontology.jsonld) the task @type is set to a relative path (i.e. just the class Type no ns prefix)
    #  SAL Modules can expect that all json uses keys corresponding to resolvable terms in the ontology's @context.
     
    salmodule_tasks = {
        "GeoconnexReferenceFeatureStates":  geoconnex_reference_feature_states
    }
    if task_name not in salmodule_tasks:
        print_err_msg(f"Task subclass '{task_name}' is not recognized.")
        sys.exit(1)

    
    return salmodule_tasks[task_name]

if __name__ == "__main__":
    cli()


