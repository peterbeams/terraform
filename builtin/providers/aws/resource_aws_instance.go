package aws

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform/helper/hashcode"
	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/mitchellh/goamz/ec2"
)

func resourceAwsInstance() *schema.Resource {
	return &schema.Resource{
		Create: resourceAwsInstanceCreate,
		Read:   resourceAwsInstanceRead,
		Update: resourceAwsInstanceUpdate,
		Delete: resourceAwsInstanceDelete,

		Schema: map[string]*schema.Schema{
			"ami": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"associate_public_ip_address": &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
				ForceNew: true,
			},

			"availability_zone": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},

			"instance_type": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"key_name": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Computed: true,
			},

			"subnet_id": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},

			"private_ip": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Computed: true,
			},

			"source_dest_check": &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
			},

			"user_data": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				StateFunc: func(v interface{}) string {
					switch v.(type) {
					case string:
						hash := sha1.Sum([]byte(v.(string)))
						return hex.EncodeToString(hash[:])
					default:
						return ""
					}
				},
			},

			"security_groups": &schema.Schema{
				Type:     schema.TypeSet,
				Optional: true,
				Computed: true,
				ForceNew: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Set: func(v interface{}) int {
					return hashcode.String(v.(string))
				},
			},

			"public_dns": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"public_ip": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"private_dns": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},

			"ebs_optimized": &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
			},

			"iam_instance_profile": &schema.Schema{
				Type:     schema.TypeString,
				ForceNew: true,
				Optional: true,
			},

			"tenancy": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},

			"tags": tagsSchema(),

			"ebs_block_device": &schema.Schema{
				Type:     schema.TypeSet,
				Optional: true,
				Computed: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"device_name": &schema.Schema{
							Type:     schema.TypeString,
							Required: true,
							ForceNew: true,
						},

						"snapshot_id": &schema.Schema{
							Type:     schema.TypeString,
							Optional: true,
							Computed: true,
							ForceNew: true,
						},

						"volume_type": &schema.Schema{
							Type:     schema.TypeString,
							Optional: true,
							Computed: true,
							ForceNew: true,
						},

						"volume_size": &schema.Schema{
							Type:     schema.TypeInt,
							Optional: true,
							Computed: true,
							ForceNew: true,
						},

						"delete_on_termination": &schema.Schema{
							Type:     schema.TypeBool,
							Optional: true,
							Default:  true,
							ForceNew: true,
						},

						"encrypted": &schema.Schema{
							Type:     schema.TypeBool,
							Optional: true,
							Computed: true,
							ForceNew: true,
						},
					},
				},
				Set: func(v interface{}) int {
					m := v.(map[string]interface{})
					return hashcode.String(m["device_name"].(string))
				},
			},

			"ephemeral_block_device": &schema.Schema{
				Type:     schema.TypeSet,
				Optional: true,
				Computed: true,
				ForceNew: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"device_name": &schema.Schema{
							Type:     schema.TypeString,
							Required: true,
							ForceNew: true,
						},

						"virtual_name": &schema.Schema{
							Type:     schema.TypeString,
							Required: true,
							ForceNew: true,
						},
					},
				},
				Set: func(v interface{}) int {
					m := v.(map[string]interface{})
					return hashcode.String(m["virtual_name"].(string))
				},
			},

			"root_block_device": &schema.Schema{
				// TODO: This is a set because we don't support singleton
				//       sub-resources today. We'll enforce that the set only ever has
				//       length zero or one below. When TF gains support for
				//       sub-resources this can be converted.
				Type:     schema.TypeSet,
				Optional: true,
				Computed: true,
				Elem: &schema.Resource{
					// "You can only modify the volume size, volume type, and Delete on
					// Termination flag on the block device mapping entry for the root
					// device volume." - bit.ly/ec2bdmap
					Schema: map[string]*schema.Schema{
						"delete_on_termination": &schema.Schema{
							Type:     schema.TypeBool,
							Optional: true,
							Default:  true,
							ForceNew: true,
						},

						"device_name": &schema.Schema{
							Type:     schema.TypeString,
							Optional: true,
							ForceNew: true,
							Default:  "/dev/sda1",
						},

						"volume_size": &schema.Schema{
							Type:     schema.TypeInt,
							Optional: true,
							Computed: true,
							ForceNew: true,
						},

						"volume_type": &schema.Schema{
							Type:     schema.TypeString,
							Optional: true,
							Computed: true,
							ForceNew: true,
						},
					},
				},
				Set: func(v interface{}) int {
					m := v.(map[string]interface{})
					return hashcode.String(m["device_name"].(string))
				},
			},
		},
	}
}

func resourceAwsInstanceCreate(d *schema.ResourceData, meta interface{}) error {
	ec2conn := meta.(*AWSClient).ec2conn

	// Figure out user data
	userData := ""
	if v := d.Get("user_data"); v != nil {
		userData = v.(string)
	}

	associatePublicIPAddress := false
	if v := d.Get("associate_public_ip_address"); v != nil {
		associatePublicIPAddress = v.(bool)
	}

	// Build the creation struct
	runOpts := &ec2.RunInstances{
		ImageId:                  d.Get("ami").(string),
		AvailZone:                d.Get("availability_zone").(string),
		InstanceType:             d.Get("instance_type").(string),
		KeyName:                  d.Get("key_name").(string),
		SubnetId:                 d.Get("subnet_id").(string),
		PrivateIPAddress:         d.Get("private_ip").(string),
		AssociatePublicIpAddress: associatePublicIPAddress,
		UserData:                 []byte(userData),
		EbsOptimized:             d.Get("ebs_optimized").(bool),
		IamInstanceProfile:       d.Get("iam_instance_profile").(string),
		Tenancy:                  d.Get("tenancy").(string),
	}

	if v := d.Get("security_groups"); v != nil {
		for _, v := range v.(*schema.Set).List() {
			str := v.(string)

			var g ec2.SecurityGroup
			if runOpts.SubnetId != "" {
				g.Id = str
			} else {
				g.Name = str
			}

			runOpts.SecurityGroups = append(runOpts.SecurityGroups, g)
		}
	}

	blockDevices := make([]ec2.BlockDeviceMapping, 0)

	if v, ok := d.GetOk("ebs_block_device"); ok {
		vL := v.(*schema.Set).List()
		for _, v := range vL {
			bd := v.(map[string]interface{})
			blockDevices = append(blockDevices, ec2.BlockDeviceMapping{
				DeleteOnTermination: bd["delete_on_termination"].(bool),
				DeviceName:          bd["device_name"].(string),
				Encrypted:           bd["encrypted"].(bool),
				SnapshotId:          bd["snapshot_id"].(string),
				VolumeSize:          int64(bd["volume_size"].(int)),
				VolumeType:          bd["volume_type"].(string),
			})
		}
	}

	if v, ok := d.GetOk("ephemeral_block_device"); ok {
		vL := v.(*schema.Set).List()
		for _, v := range vL {
			bd := v.(map[string]interface{})
			blockDevices = append(blockDevices, ec2.BlockDeviceMapping{
				DeviceName:  bd["device_name"].(string),
				VirtualName: bd["virtual_name"].(string),
			})
		}
	}

	if v, ok := d.GetOk("root_block_device"); ok {
		vL := v.(*schema.Set).List()
		if len(vL) > 1 {
			return fmt.Errorf("Cannot specify more than one root_block_device.")
		}
		for _, v := range vL {
			bd := v.(map[string]interface{})
			blockDevices = append(blockDevices, ec2.BlockDeviceMapping{
				DeleteOnTermination: bd["delete_on_termination"].(bool),
				DeviceName:          bd["device_name"].(string),
				VolumeSize:          int64(bd["volume_size"].(int)),
				VolumeType:          bd["volume_type"].(string),
			})
		}
	}

	if len(blockDevices) > 0 {
		runOpts.BlockDevices = blockDevices
	}

	// Create the instance
	log.Printf("[DEBUG] Run configuration: %#v", runOpts)
	runResp, err := ec2conn.RunInstances(runOpts)
	if err != nil {
		return fmt.Errorf("Error launching source instance: %s", err)
	}

	instance := &runResp.Instances[0]
	log.Printf("[INFO] Instance ID: %s", instance.InstanceId)

	// Store the resulting ID so we can look this up later
	d.SetId(instance.InstanceId)

	// Wait for the instance to become running so we can get some attributes
	// that aren't available until later.
	log.Printf(
		"[DEBUG] Waiting for instance (%s) to become running",
		instance.InstanceId)

	stateConf := &resource.StateChangeConf{
		Pending:    []string{"pending"},
		Target:     "running",
		Refresh:    InstanceStateRefreshFunc(ec2conn, instance.InstanceId),
		Timeout:    10 * time.Minute,
		Delay:      10 * time.Second,
		MinTimeout: 3 * time.Second,
	}

	instanceRaw, err := stateConf.WaitForState()
	if err != nil {
		return fmt.Errorf(
			"Error waiting for instance (%s) to become ready: %s",
			instance.InstanceId, err)
	}

	instance = instanceRaw.(*ec2.Instance)

	// Initialize the connection info
	d.SetConnInfo(map[string]string{
		"type": "ssh",
		"host": instance.PublicIpAddress,
	})

	// Set our attributes
	if err := resourceAwsInstanceRead(d, meta); err != nil {
		return err
	}

	// Update if we need to
	return resourceAwsInstanceUpdate(d, meta)
}

func resourceAwsInstanceRead(d *schema.ResourceData, meta interface{}) error {
	ec2conn := meta.(*AWSClient).ec2conn

	resp, err := ec2conn.Instances([]string{d.Id()}, ec2.NewFilter())
	if err != nil {
		// If the instance was not found, return nil so that we can show
		// that the instance is gone.
		if ec2err, ok := err.(*ec2.Error); ok && ec2err.Code == "InvalidInstanceID.NotFound" {
			d.SetId("")
			return nil
		}

		// Some other error, report it
		return err
	}

	// If nothing was found, then return no state
	if len(resp.Reservations) == 0 {
		d.SetId("")
		return nil
	}

	instance := &resp.Reservations[0].Instances[0]

	// If the instance is terminated, then it is gone
	if instance.State.Name == "terminated" {
		d.SetId("")
		return nil
	}

	d.Set("availability_zone", instance.AvailZone)
	d.Set("key_name", instance.KeyName)
	d.Set("public_dns", instance.DNSName)
	d.Set("public_ip", instance.PublicIpAddress)
	d.Set("private_dns", instance.PrivateDNSName)
	d.Set("private_ip", instance.PrivateIpAddress)
	d.Set("subnet_id", instance.SubnetId)
	d.Set("ebs_optimized", instance.EbsOptimized)
	d.Set("tags", tagsToMap(instance.Tags))
	d.Set("tenancy", instance.Tenancy)

	// Determine whether we're referring to security groups with
	// IDs or names. We use a heuristic to figure this out. By default,
	// we use IDs if we're in a VPC. However, if we previously had an
	// all-name list of security groups, we use names. Or, if we had any
	// IDs, we use IDs.
	useID := instance.SubnetId != ""
	if v := d.Get("security_groups"); v != nil {
		match := false
		for _, v := range v.(*schema.Set).List() {
			if strings.HasPrefix(v.(string), "sg-") {
				match = true
				break
			}
		}

		useID = match
	}

	// Build up the security groups
	sgs := make([]string, len(instance.SecurityGroups))
	for i, sg := range instance.SecurityGroups {
		if useID {
			sgs[i] = sg.Id
		} else {
			sgs[i] = sg.Name
		}
	}
	d.Set("security_groups", sgs)

	if err := readBlockDevices(d, instance, ec2conn); err != nil {
		return err
	}

	return nil
}

func resourceAwsInstanceUpdate(d *schema.ResourceData, meta interface{}) error {
	ec2conn := meta.(*AWSClient).ec2conn
	opts := new(ec2.ModifyInstance)

	opts.SetSourceDestCheck = true
	opts.SourceDestCheck = d.Get("source_dest_check").(bool)

	log.Printf("[INFO] Modifying instance %s: %#v", d.Id(), opts)
	if _, err := ec2conn.ModifyInstance(d.Id(), opts); err != nil {
		return err
	}

	// TODO(mitchellh): wait for the attributes we modified to
	// persist the change...

	if err := setTags(ec2conn, d); err != nil {
		return err
	} else {
		d.SetPartial("tags")
	}

	return nil
}

func resourceAwsInstanceDelete(d *schema.ResourceData, meta interface{}) error {
	ec2conn := meta.(*AWSClient).ec2conn

	log.Printf("[INFO] Terminating instance: %s", d.Id())
	if _, err := ec2conn.TerminateInstances([]string{d.Id()}); err != nil {
		return fmt.Errorf("Error terminating instance: %s", err)
	}

	log.Printf(
		"[DEBUG] Waiting for instance (%s) to become terminated",
		d.Id())

	stateConf := &resource.StateChangeConf{
		Pending:    []string{"pending", "running", "shutting-down", "stopped", "stopping"},
		Target:     "terminated",
		Refresh:    InstanceStateRefreshFunc(ec2conn, d.Id()),
		Timeout:    10 * time.Minute,
		Delay:      10 * time.Second,
		MinTimeout: 3 * time.Second,
	}

	_, err := stateConf.WaitForState()
	if err != nil {
		return fmt.Errorf(
			"Error waiting for instance (%s) to terminate: %s",
			d.Id(), err)
	}

	d.SetId("")
	return nil
}

// InstanceStateRefreshFunc returns a resource.StateRefreshFunc that is used to watch
// an EC2 instance.
func InstanceStateRefreshFunc(conn *ec2.EC2, instanceID string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		resp, err := conn.Instances([]string{instanceID}, ec2.NewFilter())
		if err != nil {
			if ec2err, ok := err.(*ec2.Error); ok && ec2err.Code == "InvalidInstanceID.NotFound" {
				// Set this to nil as if we didn't find anything.
				resp = nil
			} else {
				log.Printf("Error on InstanceStateRefresh: %s", err)
				return nil, "", err
			}
		}

		if resp == nil || len(resp.Reservations) == 0 || len(resp.Reservations[0].Instances) == 0 {
			// Sometimes AWS just has consistency issues and doesn't see
			// our instance yet. Return an empty state.
			return nil, "", nil
		}

		i := &resp.Reservations[0].Instances[0]
		return i, i.State.Name, nil
	}
}

func readBlockDevices(d *schema.ResourceData, instance *ec2.Instance, ec2conn *ec2.EC2) error {
	ibds, err := readBlockDevicesFromInstance(instance, ec2conn)
	if err != nil {
		return err
	}

	d.Set("ebs_block_device", ibds["ebs"])
	d.Set("root_block_device", []interface{}{ibds["root"]})
	seenDeviceNames := ibds["seen"].(map[string]bool)

	// If "ephemeral_block_device" is non-empty, either it was specified in the
	// config, or it was populated by a prior run of the provider's Read function.
	if _, ok := d.GetOk("ephemeral_block_device"); !ok {
		if ebds, err := readEphemeralBlockDevicesFromAMI(instance.ImageId, ec2conn); err != nil {
			// Ephemeral BDs from the AMI are not attached if their device name conflicts with another configure BD.
			attachedEBDs := make([]map[string]interface{}, 0)
			for _, ebd := range ebds {
				if !seenDeviceNames[ebd["device_name"].(string)] {
					attachedEBDs = append(attachedEBDs, ebd)
				}
			}
			d.Set("ephemeral_block_device", attachedEBDs)
		} else {
			return err
		}
	}

	return nil
}

func readBlockDevicesFromInstance(instance *ec2.Instance, ec2conn *ec2.EC2) (map[string]interface{}, error) {
	blockDevices := make(map[string]interface{})
	blockDevices["ebs"] = make([]map[string]interface{}, 0)
	blockDevices["ephemeral"] = make([]map[string]interface{}, 0)
	blockDevices["root"] = nil
	blockDevices["seen"] = make(map[string]bool)

	instanceBlockDevices := make(map[string]ec2.BlockDevice)
	for _, bd := range instance.BlockDevices {
		instanceBlockDevices[bd.VolumeId] = bd
	}

	volIDs := make([]string, 0, len(instanceBlockDevices))
	for volID := range instanceBlockDevices {
		volIDs = append(volIDs, volID)
	}

	// Need to call DescribeVolumes to get volume_size and volume_type for each
	// EBS block device
	volResp, err := ec2conn.Volumes(volIDs, ec2.NewFilter())
	if err != nil {
		return nil, err
	}

	for _, vol := range volResp.Volumes {
		instanceBd := instanceBlockDevices[vol.VolumeId]
		bd := make(map[string]interface{})
		volSize, err := strconv.Atoi(vol.Size)
		if err != nil {
			return nil, err
		}

		bd["delete_on_termination"] = instanceBd.DeleteOnTermination
		bd["device_name"] = instanceBd.DeviceName
		bd["volume_size"] = volSize
		bd["volume_type"] = vol.VolumeType

		if instanceBd.DeviceName == instance.RootDeviceName {
			blockDevices["root"] = bd
		} else {
			bd["encrypted"] = vol.Encrypted
			bd["snapshot_id"] = vol.SnapshotId

			blockDevices["ebs"] = append(blockDevices["ebs"].([]map[string]interface{}), bd)
		}
		blockDevices["seen"].(map[string]bool)[instanceBd.DeviceName] = true
	}

	return blockDevices, nil
}

func readEphemeralBlockDevicesFromAMI(ami string, ec2conn *ec2.EC2) ([]map[string]interface{}, error) {
	ephemeralBlockDevices := make([]map[string]interface{}, 0)

	imgResp, err := ec2conn.Images([]string{ami}, ec2.NewFilter())
	if err != nil {
		return nil, err
	}
	if len(imgResp.Images) != 1 {
		return nil, fmt.Errorf("Error: %#v, expected 1 image in response: %#v", ami, imgResp)
	}

	for _, amibd := range imgResp.Images[0].BlockDevices {
		// The AMI's BlockDeviceMapping contains more than just ephemeral storage,
		// but that's all we care about here.
		if !strings.HasPrefix(amibd.VirtualName, "ephemeral") {
			continue
		}

		ebd := make(map[string]interface{})
		ebd["device_name"] = amibd.DeviceName
		ebd["virtual_name"] = amibd.VirtualName
		ephemeralBlockDevices = append(ephemeralBlockDevices, ebd)
	}

	return ephemeralBlockDevices, nil
}
