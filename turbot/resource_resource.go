package turbot

import (
	"encoding/json"
	"fmt"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/iancoleman/strcase"
	"github.com/terraform-providers/terraform-provider-turbot/apiclient"
	"log"
)

func resourceTurbotResource() *schema.Resource {
	return &schema.Resource{
		Create: resourceTurbotResourceCreate,
		Read:   resourceTurbotResourceRead,
		Update: resourceTurbotResourceUpdate,
		Delete: resourceTurbotResourceDelete,
		Exists: resourceTurbotResourceExists,
		Importer: &schema.ResourceImporter{
			State: resourceTurbotResourceImport,
		},
		Schema: map[string]*schema.Schema{
			// aka of the parent resource
			"parent": {
				Type:     schema.TypeString,
				Required: true,
				// when doing a diff, the state file will contain the id of the parent bu tthe config contains the aka,
				// so we need custom diff code
				DiffSuppressFunc: supressIfParentAkaMatches,
			},
			// when doing a read, fetch the parent akas to use in supressIfParentAkaMatches()
			"parent_akas": {
				Type:     schema.TypeList,
				Computed: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
			},
			"type": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"body": {
				Type:             schema.TypeString,
				Required:         true,
				DiffSuppressFunc: suppressIfBodyMatches,
			},
		},
	}
}

func resourceTurbotResourceExists(d *schema.ResourceData, meta interface{}) (b bool, e error) {
	client := meta.(*apiclient.Client)
	id := d.Id()
	return client.ResourceExists(id)
}

func resourceTurbotResourceCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*apiclient.Client)
	parent := d.Get("parent").(string)
	resourceType := d.Get("type").(string)
	body := d.Get("body").(string)

	// create resource returns turbot resource metadata containing the id
	turbotMetadata, err := client.CreateResource(resourceType, parent, body)
	if err != nil {
		return err
	}

	// set parent_akas property by loading parent resource and fetching the akas
	if err = setParentAkas(turbotMetadata.ParentId, d, meta); err != nil {
		return err
	}

	// assign the id
	d.SetId(turbotMetadata.Id)
	// save formatted version of the body for consistency
	d.Set("body", formatBody(body))

	return nil
}

func resourceTurbotResourceRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*apiclient.Client)
	id := d.Id()

	// build required properties from body
	properties, err := propertiesFromBody(d.Get("body").(string))
	if err != nil {
		return fmt.Errorf("error retrieving properties from resource body: %s", err.Error())
	}

	resource, err := client.ReadResource(id, properties)
	if err != nil {
		if apiclient.NotFoundError(err) {
			// resource was not found - clear id
			d.SetId("")
		}
		return err
	}

	// rebuild body from the resource
	body, err := bodyFromProperties(resource.Data)
	if err != nil {
		return fmt.Errorf("error building resource body: %s", err.Error())
	}

	// assign results back into ResourceData

	// set parent_akas property by loading parent resource and fetching the akas
	if err = setParentAkas(resource.Turbot.ParentId, d, meta); err != nil {
		return err
	}
	d.Set("parent", resource.Turbot.ParentId)
	d.Set("body", body)

	return nil
}

func resourceTurbotResourceUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*apiclient.Client)
	body := d.Get("body").(string)
	parent := d.Get("parent").(string)
	resourceType := d.Get("type").(string)
	id := d.Id()
	// create folder returns turbot resource metadata containing the id
	turbotMetadata, err := client.UpdateResource(id, resourceType, parent, body)
	if err != nil {
		return err
	}
	// set parent_akas property by loading parent resource and fetching the akas
	if err = setParentAkas(turbotMetadata.ParentId, d, meta); err != nil {
		return err
	}
	return nil
}

func resourceTurbotResourceDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*apiclient.Client)
	id := d.Id()
	err := client.DeleteResource(id)
	if err != nil {
		return err
	}

	// clear the id to show we have deleted
	d.SetId("")

	return nil
}

func resourceTurbotResourceImport(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	if err := resourceTurbotResourceRead(d, meta); err != nil {
		return nil, err
	}
	return []*schema.ResourceData{d}, nil
}

func setParentAkas(parentId string, d *schema.ResourceData, meta interface{}) error {
	client := meta.(*apiclient.Client)

	// load parent resource to get parent_akas
	parent, err := client.ReadResource(parentId, nil)
	if err != nil {
		log.Printf("[ERROR] Failed to load parentAka resource; %s", err)
		return err
	}
	parentAkas := parent.Turbot.Akas
	// if this resource has no akas, just use the id
	if parentAkas == nil {
		parentAkas = []string{parentId}
	}

	// assign parent_akas
	d.Set("parent_akas", parent.Turbot.Akas)
	return nil
}

// the 'parent' in the config is an aka - however the state file will have an id.
// to perform a diff we also store parent_akas in state file, which is the list of akas for the parent
// if the new value of parent existts in parent_akas, then suppress diff
func supressIfParentAkaMatches(k, old, new string, d *schema.ResourceData) bool {
	parentAkasProperty, parentAkasSet := d.GetOk("parent_akas")
	// if parent_id has not been set yet, do not suppress the diff
	if !parentAkasSet {
		return false
	}

	parentAkas, ok := parentAkasProperty.([]interface{})
	if !ok {
		return false
	}
	// if parentAkas contains 'new', suppress diff
	for _, aka := range parentAkas {
		if aka.(string) == new {
			return true
		}
	}
	return false
}

func suppressIfClientSecret(k, old, new string, d *schema.ResourceData) bool {
	return old != ""
}

// body is a json string
// apply standard formatting to old and new bodys then compare
func suppressIfBodyMatches(k, old, new string, d *schema.ResourceData) bool {
	if old == "" || new == "" {
		return false
	}
	return formatBody(old) == formatBody(new)
}

// given a json string, unmarshal into a map and return a map of alias ->  propertyName
func propertiesFromBody(body string) (map[string]string, error) {
	data := map[string]interface{}{}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return nil, err
	}
	var properties = map[string]string{}
	for k := range data {
		properties[k] = k
	}
	return properties, nil
}

// given a map of resource properties, marshal into a json string
func bodyFromProperties(d map[string]interface{}) (string, error) {
	body, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// apply standard formatting to the json body to enable easy diffing
func formatBody(body string) string {
	data := map[string]interface{}{}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		// ignore error and just return original body
		return body
	}
	body, err := bodyFromProperties(data)
	if err != nil {
		// ignore error and just return original body
		return body
	}
	return body

}

func mapFromResourceData(d *schema.ResourceData, properties []string) map[string]interface{} {
	var propertyMap = map[string]interface{}{}
	for _, terraformProperty := range properties {
		// get schema for property
		value, propertySet := d.GetOk(terraformProperty)
		if propertySet {
			// converted property from snake case (Terraform format) to lowerCamelCase (Turbot format).
			var turbotProperty = strcase.ToLowerCamel(terraformProperty)
			propertyMap[turbotProperty] = value
		}
	}
	return propertyMap
}
