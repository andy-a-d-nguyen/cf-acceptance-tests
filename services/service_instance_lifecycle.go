package services_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cloudfoundry/cf-acceptance-tests/cats_suite_helpers"

	"github.com/cloudfoundry/cf-test-helpers/v2/cf"
	"github.com/cloudfoundry/cf-test-helpers/v2/helpers"

	"github.com/cloudfoundry/cf-acceptance-tests/helpers/app_helpers"
	"github.com/cloudfoundry/cf-acceptance-tests/helpers/assets"
	"github.com/cloudfoundry/cf-acceptance-tests/helpers/random_name"
	"github.com/cloudfoundry/cf-acceptance-tests/helpers/services"
	. "github.com/cloudfoundry/cf-acceptance-tests/helpers/services"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gbytes"
	. "github.com/onsi/gomega/gexec"
)

var _ = ServicesDescribe("Service Instance Lifecycle", func() {
	const asyncOperationPollInterval = 5 * time.Second
	var broker services.ServiceBroker

	waitForAsyncDeletionToComplete := func(broker services.ServiceBroker, instanceName string) {
		Eventually(func() *Buffer {
			session := cf.Cf("service", instanceName).Wait()
			combinedOutputBytes := append(session.Out.Contents(), session.Err.Contents()...)
			return BufferWithBytes(combinedOutputBytes)
		}, Config.AsyncServiceOperationTimeoutDuration(), asyncOperationPollInterval).Should(Say("not found"))
	}

	waitForAsyncOperationToCompleteAndSay := func(broker services.ServiceBroker, instanceName, expectedText string) {
		Eventually(func() *Session {
			serviceDetails := cf.Cf("service", instanceName).Wait()
			Expect(serviceDetails).To(Exit(0), "failed getting service instance details")
			return serviceDetails
		}, Config.AsyncServiceOperationTimeoutDuration(), asyncOperationPollInterval).Should(Say(expectedText))
	}

	Describe("Synchronous operations", func() {
		BeforeEach(func() {
			broker = services.NewServiceBroker(
				random_name.CATSRandomName("BRKR"),
				assets.NewAssets().ServiceBroker,
				TestSetup,
			)
			broker.Push(Config)
			broker.Configure()
			broker.Create()
			broker.PublicizePlans()
		})

		AfterEach(func() {
			app_helpers.AppReport(broker.Name)

			broker.Destroy()
		})

		Describe("just service instances", func() {
			var instanceName string
			AfterEach(func() {
				if instanceName != "" {
					Expect(cf.Cf("delete-service", instanceName, "-f").Wait()).To(Exit(0))
				}
			})

			It("can create a service instance", func() {
				tags := "['tag1', 'tag2']"
				params := "{\"param1\": \"value\"}"

				instanceName = random_name.CATSRandomName("SVIN")
				createService := cf.Cf("create-service", broker.Service.Name, broker.SyncPlans[0].Name, instanceName, "-c", params, "-t", tags).Wait()
				Expect(createService).To(Exit(0))

				serviceInfo := cf.Cf("-v", "service", instanceName).Wait()
				Expect(serviceInfo).To(Say("[P|p]lan:\\s+%s", broker.SyncPlans[0].Name))
				Expect(serviceInfo.Out.Contents()).To(MatchRegexp(`"tags":\s*\[\n.*tag1.*\n.*tag2.*\n.*\]`))
			})

			Context("when there is an existing service instance", func() {
				BeforeEach(func() {
					params := "{\"param1\": \"value\"}"
					instanceName = random_name.CATSRandomName("SVIN")
					createService := cf.Cf("create-service", broker.Service.Name, broker.SyncPlans[0].Name, instanceName, "-c", params).Wait()
					Expect(createService).To(Exit(0), "failed creating service")
				})

				It("fetch the configuration parameters", func() {
					instanceGUID := getGuidFor("service", instanceName)
					configParams := cf.Cf("curl", fmt.Sprintf("/v3/service_instances/%s/parameters", instanceGUID)).Wait()
					Expect(configParams).To(Exit(0), "failed to curl fetch binding parameters")
					Expect(configParams.Out.Contents()).To(MatchJSON("{\"param1\": \"value\"}"))
				})

				It("can delete a service instance", func() {
					deleteService := cf.Cf("delete-service", instanceName, "-f").Wait()
					Expect(deleteService).To(Exit(0))

					serviceInfo := cf.Cf("service", instanceName).Wait()
					combinedBuffer := BufferWithBytes(append(serviceInfo.Out.Contents(), serviceInfo.Err.Contents()...))
					Expect(combinedBuffer).To(Say("not found"))
				})

				Context("updating a service instance", func() {
					tags := "['tag1', 'tag2']"
					params := "{\"param1\": \"value\"}"

					It("can rename a service", func() {
						newname := random_name.CATSRandomName("SVC-RENAME")
						updateService := cf.Cf("rename-service", instanceName, newname).Wait()
						Expect(updateService).To(Exit(0))

						serviceInfo := cf.Cf("service", newname).Wait()
						Expect(serviceInfo).To(Say(newname))

						serviceInfo = cf.Cf("service", instanceName).Wait()
						Expect(serviceInfo).To(Exit(1))
					})

					It("can update a service plan", func() {
						updateService := cf.Cf("update-service", instanceName, "-p", broker.SyncPlans[1].Name).Wait()
						Expect(updateService).To(Exit(0))

						serviceInfo := cf.Cf("service", instanceName).Wait()
						Expect(serviceInfo).To(Say("[P|p]lan:\\s+%s", broker.SyncPlans[1].Name))
					})

					It("can update service tags", func() {
						updateService := cf.Cf("update-service", instanceName, "-t", tags).Wait()
						Expect(updateService).To(Exit(0))

						serviceInfo := cf.Cf("-v", "service", instanceName).Wait()
						Expect(serviceInfo.Out.Contents()).To(MatchRegexp(`"tags":\s*\[\n.*tag1.*\n.*tag2.*\n.*\]`))
					})

					It("can update arbitrary parameters", func() {
						updateService := cf.Cf("update-service", instanceName, "-c", params).Wait()
						Expect(updateService).To(Exit(0), "Failed updating service")
						//Note: We don't necessarily get these back through a service instance lookup
					})

					It("can update all available parameters at once", func() {
						updateService := cf.Cf(
							"update-service", instanceName,
							"-p", broker.SyncPlans[1].Name,
							"-t", tags,
							"-c", params).Wait()
						Expect(updateService).To(Exit(0))

						serviceInfo := cf.Cf("-v", "service", instanceName).Wait()
						Expect(serviceInfo).To(Say("[P|p]lan:\\s+%s", broker.SyncPlans[1].Name))
						Expect(serviceInfo.Out.Contents()).To(MatchRegexp(`"tags":\s*\[\n.*tag1.*\n.*tag2.*\n.*\]`))
					})
				})

				Describe("service keys", func() {
					var keyName string
					BeforeEach(func() {
						keyName = random_name.CATSRandomName("SVC-KEY")
					})

					AfterEach(func() {
						Expect(cf.Cf("delete-service-key", instanceName, keyName, "-f").Wait()).To(Exit(0))
					})

					It("can create service keys", func() {
						createKey := cf.Cf("create-service-key", instanceName, keyName).Wait()
						Expect(createKey).To(Exit(0), "failed to create key")

						keyInfo := cf.Cf("service-key", instanceName, keyName).Wait()
						Expect(keyInfo).To(Exit(0), "failed key info")

						Expect(keyInfo).To(Say(`"database": "fake-dbname"`))
						Expect(keyInfo).To(Say(`"password": "fake-password"`))
						Expect(keyInfo).To(Say(`"username": "fake-user"`))
					})

					It("can create service keys with arbitrary params", func() {
						params := "{\"param1\": \"value\"}"
						createKey := cf.Cf("create-service-key", instanceName, keyName, "-c", params).Wait()
						Expect(createKey).To(Exit(0), "failed creating key with params")
					})

					Context("when there is an existing key", func() {
						BeforeEach(func() {
							params := "{\"param1\": \"value\"}"
							createKey := cf.Cf("create-service-key", instanceName, keyName, "-c", params).Wait()
							Expect(createKey).To(Exit(0), "failed to create key")
						})

						It("can retrieve parameters", func() {
							serviceKeyGUID := getGuidFor("service-key", instanceName, keyName)
							paramsEndpoint := fmt.Sprintf("/v3/service_credential_bindings/%s/parameters", serviceKeyGUID)

							fetchServiceKeyParameters := cf.Cf("curl", paramsEndpoint).Wait()
							Expect(fetchServiceKeyParameters.Out.Contents()).To(MatchJSON(`{"param1": "value"}`))
							Expect(fetchServiceKeyParameters).To(Exit(0), "failed to fetch service key parameters")
						})

						It("can delete the key", func() {
							deleteServiceKey := cf.Cf("delete-service-key", instanceName, keyName, "-f").Wait()
							Expect(deleteServiceKey).To(Exit(0), "failed deleting service key")

							keyInfo := cf.Cf("service-key", instanceName, keyName).Wait()
							output := append(keyInfo.Out.Contents(), keyInfo.Err.Contents()...)
							Expect(output).To(ContainSubstring("No service key %s found for service instance %s", keyName, instanceName))
						})
					})
				})
			})
		})

		Context("when there is an app", func() {
			var instanceName, appName string

			BeforeEach(func() {
				appName = random_name.CATSRandomName("APP")
				createApp := cf.Cf(app_helpers.CatnipWithArgs(
					appName,
					"-m", DEFAULT_MEMORY_LIMIT)...,
				).Wait(Config.CfPushTimeoutDuration())
				Expect(createApp).To(Exit(0), "failed creating app")

				checkForAppEvent(appName, "audit.app.create")

				instanceName = random_name.CATSRandomName("SVIN")
				createService := cf.Cf("create-service", broker.Service.Name, broker.SyncPlans[0].Name, instanceName).Wait()
				Expect(createService).To(Exit(0), "failed creating service")
			})

			AfterEach(func() {
				app_helpers.AppReport(appName)
				Expect(cf.Cf("delete", appName, "-f", "-r").Wait(Config.CfPushTimeoutDuration())).To(Exit(0))
				Expect(cf.Cf("delete-service", instanceName, "-f").Wait()).To(Exit(0))
			})

			Describe("bindings", func() {
				It("can bind service to app and check app env and events", func() {
					bindService := cf.Cf("bind-service", appName, instanceName).Wait()
					Expect(bindService).To(Exit(0), "failed binding app to service")

					checkForAppEvent(appName, "audit.app.update")

					appEnv := cf.Cf("env", appName).Wait()
					Expect(appEnv).To(Exit(0), "failed get env for app")
					Expect(appEnv).To(Say("credentials"))

					restartApp := cf.Cf("restart", appName).Wait(Config.CfPushTimeoutDuration())
					Expect(restartApp).To(Exit(0), "failed restarting app")

					Expect(helpers.CurlApp(Config, appName, "/env/VCAP_SERVICES")).Should(ContainSubstring("fake-service://fake-user:fake-password@fake-host:3306/fake-dbname"))
				})

				It("can bind service to app and send arbitrary params", func() {
					bindService := cf.Cf("bind-service", appName, instanceName, "-c", `{"param1": "value"}`).Wait()
					Expect(bindService).To(Exit(0), "failed binding app to service")
				})

				Context("when there is an existing binding", func() {
					BeforeEach(func() {
						bindService := cf.Cf("bind-service", appName, instanceName, "-c", `{"max_clients": 5}`).Wait()
						Expect(bindService).To(Exit(0), "failed binding app to service")
					})

					It("can retrieve parameters", func() {
						appGUID := app_helpers.GetAppGuid(appName)
						serviceInstanceGUID := getGuidFor("service", instanceName)
						paramsEndpoint := getBindingParamsEndpoint(appGUID, serviceInstanceGUID)

						fetchBindingParameters := cf.Cf("curl", paramsEndpoint).Wait()
						Expect(fetchBindingParameters.Out.Contents()).To(MatchJSON(`{"max_clients": 5}`))
						Expect(fetchBindingParameters).To(Exit(0), "failed to fetch binding parameters")
					})

					It("can unbind service to app and check app env and events", func() {
						unbindService := cf.Cf("unbind-service", appName, instanceName).Wait()
						Expect(unbindService).To(Exit(0), "failed unbinding app to service")

						checkForAppEvent(appName, "audit.app.update")

						appEnv := cf.Cf("env", appName).Wait()
						Expect(appEnv).To(Exit(0), "failed get env for app")
						Expect(appEnv).ToNot(Say("credentials"))

						restartApp := cf.Cf("restart", appName).Wait(Config.CfPushTimeoutDuration())
						Expect(restartApp).To(Exit(0), "failed restarting app")

						Expect(helpers.CurlApp(Config, appName, "/env/VCAP_SERVICES")).ShouldNot(ContainSubstring("fake-service://fake-user:fake-password@fake-host:3306/fake-dbname"))
					})
				})
			})
		})
	})

	Describe("Asynchronous operations", func() {
		var instanceName string

		BeforeEach(func() {
			broker = services.NewServiceBroker(
				random_name.CATSRandomName("BRKR"),
				assets.NewAssets().ServiceBroker,
				TestSetup,
			)
			broker.Push(Config)
			broker.Configure()
			broker.Create()
			broker.PublicizePlans()
		})

		AfterEach(func() {
			app_helpers.AppReport(broker.Name)

			Expect(cf.Cf("delete-service", instanceName, "-f").Wait()).To(Exit())
			waitForAsyncDeletionToComplete(broker, instanceName)

			broker.Destroy()
		})

		Describe("for a service instance", func() {
			It("can create a service instance", func() {
				tags := "['tag1', 'tag2']"
				params := "{\"param1\": \"value\"}"

				instanceName = random_name.CATSRandomName("SVIN")
				createService := cf.Cf("create-service", broker.Service.Name, broker.AsyncPlans[0].Name, instanceName, "-t", tags, "-c", params).Wait()
				Expect(createService).To(Exit(0))
				Expect(createService).To(Say("Create in progress."))

				waitForAsyncOperationToCompleteAndSay(broker, instanceName, "succeeded")

				serviceInfo := cf.Cf("-v", "service", instanceName).Wait()
				Expect(serviceInfo).To(Say("[P|p]lan:\\s+%s", broker.AsyncPlans[0].Name))
				Expect(serviceInfo).To(Say("[S|s]tatus:\\s+create succeeded"))
				Expect(serviceInfo).To(Say("[M|m]essage:\\s+100 percent done"))
				Expect(serviceInfo.Out.Contents()).To(MatchRegexp(`"tags":\s*\[\n.*tag1.*\n.*tag2.*\n.*\]`))
			})

			Context("when there is an existing service instance", func() {
				tags := "['tag1', 'tag2']"
				params := "{\"param1\": \"value2\"}"

				BeforeEach(func() {
					instanceName = random_name.CATSRandomName("SVC")
					createService := cf.Cf("create-service", broker.Service.Name, broker.AsyncPlans[0].Name, instanceName).Wait()
					Expect(createService).To(Exit(0))
					Expect(createService).To(Say("Create in progress."))

					waitForAsyncOperationToCompleteAndSay(broker, instanceName, "succeeded")
				})

				It("can update a service plan", func() {
					updateService := cf.Cf("update-service", instanceName, "-p", broker.AsyncPlans[1].Name).Wait()
					Expect(updateService).To(Exit(0))
					Expect(updateService).To(Say("Update in progress."))

					serviceInfo := cf.Cf("service", instanceName).Wait()
					Expect(serviceInfo).To(Exit(0), "failed getting service instance details")
					Expect(serviceInfo).To(Say("[P|p]lan:\\s+%s", broker.AsyncPlans[0].Name))

					waitForAsyncOperationToCompleteAndSay(broker, instanceName, "succeeded")

					serviceInfo = cf.Cf("service", instanceName).Wait()
					Expect(serviceInfo).To(Exit(0), "failed getting service instance details")
					Expect(serviceInfo).To(Say("[P|p]lan:\\s+%s", broker.AsyncPlans[1].Name))
				})

				It("can update the arbitrary params", func() {
					updateService := cf.Cf("update-service", instanceName, "-c", params).Wait()
					Expect(updateService).To(Exit(0))
					Expect(updateService).To(Say("Update in progress."))

					waitForAsyncOperationToCompleteAndSay(broker, instanceName, "succeeded")
				})

				It("can update all of the possible parameters at once", func() {
					updateService := cf.Cf(
						"update-service", instanceName,
						"-t", tags,
						"-c", params,
						"-p", broker.AsyncPlans[1].Name).Wait()
					Expect(updateService).To(Exit(0))
					Expect(updateService).To(Say("Update in progress."))

					waitForAsyncOperationToCompleteAndSay(broker, instanceName, "succeeded")

					serviceInfo := cf.Cf("-v", "service", instanceName).Wait()
					Expect(serviceInfo).To(Exit(0), "failed getting service instance details")
					Expect(serviceInfo).To(Say("[P|p]lan:\\s+%s", broker.AsyncPlans[1].Name))
					Expect(serviceInfo.Out.Contents()).To(MatchRegexp(`"tags":\s*\[\n.*tag1.*\n.*tag2.*\n.*\]`))
				})

				It("can delete a service instance", func() {
					deleteService := cf.Cf("delete-service", instanceName, "-f").Wait()
					Expect(deleteService).To(Exit(0), "failed making delete request")
					Expect(deleteService).To(Say("Delete in progress."))

					waitForAsyncDeletionToComplete(broker, instanceName)
				})

				Context("when there is an app", func() {
					var appName string
					BeforeEach(func() {
						appName = random_name.CATSRandomName("APP")
						Expect(cf.Cf(app_helpers.CatnipWithArgs(
							appName,
							"-m", DEFAULT_MEMORY_LIMIT)...,
						).Wait(Config.CfPushTimeoutDuration())).To(Exit(0), "failed creating app")
					})

					AfterEach(func() {
						app_helpers.AppReport(appName)
						Expect(cf.Cf("delete", appName, "-f", "-r").Wait(Config.CfPushTimeoutDuration())).To(Exit(0))
					})

					It("can bind a service instance", func() {
						bindService := cf.Cf("bind-service", appName, instanceName).Wait()
						Expect(bindService).To(Exit(0), "failed binding app to service")

						checkForAppEvent(appName, "audit.app.update")

						restageApp := cf.Cf("restage", appName).Wait(Config.CfPushTimeoutDuration())
						Expect(restageApp).To(Exit(0), "failed restaging app")

						checkForAppEvent(appName, "audit.app.build.create")

						appEnv := cf.Cf("env", appName).Wait()
						Expect(appEnv).To(Exit(0), "failed get env for app")
						Expect(appEnv).To(Say("credentials"))
					})

					It("can bind service to app and send arbitrary params", func() {
						bindService := cf.Cf("bind-service", appName, instanceName, "-c", params).Wait()
						Expect(bindService).To(Exit(0), "failed binding app to service")

						checkForAppEvent(appName, "audit.app.update")
					})

					Context("when there is an existing binding", func() {
						BeforeEach(func() {
							bindService := cf.Cf("bind-service", appName, instanceName).Wait()
							Expect(bindService).To(Exit(0), "failed binding app to service")
						})

						It("can unbind a service instance", func() {
							unbindService := cf.Cf("unbind-service", appName, instanceName).Wait()
							Expect(unbindService).To(Exit(0), "failed unbinding app to service")

							checkForAppEvent(appName, "audit.app.update")

							appEnv := cf.Cf("env", appName).Wait()
							Expect(appEnv).To(Exit(0), "failed get env for app")
							Expect(appEnv).ToNot(Say("credentials"))
						})
					})
				})
			})
		})

		Describe("for a service binding", func() {
			var appName string

			BeforeEach(func() {
				instanceName = random_name.CATSRandomName("SVC")
				createService := cf.Cf("create-service", broker.Service.Name, broker.AsyncPlans[2].Name, instanceName).Wait()
				Expect(createService).To(Exit(0))
				Expect(createService).To(Say("Create in progress."))

				waitForAsyncOperationToCompleteAndSay(broker, instanceName, "succeeded")

				appName = random_name.CATSRandomName("APP")
				Expect(cf.Cf(app_helpers.CatnipWithArgs(
					appName,
					"-m", DEFAULT_MEMORY_LIMIT)...,
				).Wait(Config.CfPushTimeoutDuration())).To(Exit(0), "failed pushing app")
			})

			AfterEach(func() {
				app_helpers.AppReport(appName)
				Expect(cf.Cf("delete", appName, "-f", "-r").Wait(Config.CfPushTimeoutDuration())).To(Exit(0))
			})

			It("can bind and unbind asynchronously", func() {
				By("creating a binding asynchronously")
				bindService := cf.Cf("bind-service", appName, instanceName).Wait()

				Expect(bindService).To(Exit(0), "failed to asynchronously bind service")

				By("waiting for binding to be created")
				waitForAsyncOperationToCompleteAndSay(broker, instanceName, appName+".*\\ssucceeded")

				appEnv := cf.Cf("env", appName).Wait()
				Expect(appEnv).To(Exit(0), "failed get env for app")
				Expect(appEnv).To(Say("credentials"))

				restartApp := cf.Cf("restart", appName).Wait(Config.CfPushTimeoutDuration())
				Expect(restartApp).To(Exit(0), "failed restarting app")

				Expect(helpers.CurlApp(Config, appName, "/env/VCAP_SERVICES")).Should(ContainSubstring("fake-service://fake-user:fake-password@fake-host:3306/fake-dbname"))

				By("deleting the binding asynchronously")
				unbindService := cf.Cf("unbind-service", appName, instanceName).Wait()
				Expect(unbindService).To(Exit(0), "failed to asynchronously unbind service")

				By("waiting for binding to be deleted")
				waitForAsyncOperationToCompleteAndSay(broker, instanceName, "There are no bound apps for this service.")

				appEnv = cf.Cf("env", appName).Wait()
				Expect(appEnv).To(Exit(0), "failed get env for app")
				Expect(appEnv).ToNot(Say("credentials"))

				restartApp = cf.Cf("restart", appName).Wait(Config.CfPushTimeoutDuration())
				Expect(restartApp).To(Exit(0), "failed restarting app")

				Expect(helpers.CurlApp(Config, appName, "/env/VCAP_SERVICES")).ShouldNot(ContainSubstring("fake-service://fake-user:fake-password@fake-host:3306/fake-dbname"))
			})
		})
	})
})

func checkForAppEvent(appName string, eventName string) {
	Eventually(func() string {
		return string(cf.Cf("events", appName).Wait().Out.Contents())
	}).Should(MatchRegexp(eventName))
}

func getBindingParamsEndpoint(appGUID string, instanceGUID string) string {
	jsonResults := Response{}
	bindingCurl := cf.Cf("curl", fmt.Sprintf("/v3/service_credential_bindings?app_guids=%s&service_instance_guids=%s", appGUID, instanceGUID)).Wait()
	Expect(bindingCurl).To(Exit(0))
	Expect(json.Unmarshal(bindingCurl.Out.Contents(), &jsonResults)).NotTo(HaveOccurred())

	Expect(len(jsonResults.Resources)).To(BeNumerically(">", 0), "Expected to find at least one service resource.")

	return fmt.Sprintf("/v3/service_credential_bindings/%s/parameters", jsonResults.Resources[0].GUID)
}

func getGuidFor(args ...string) string {
	args = append(args, "--guid")
	session := cf.Cf(args...).Wait()

	out := string(session.Out.Contents())
	return strings.TrimSpace(out)
}
