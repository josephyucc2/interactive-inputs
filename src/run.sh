env \
  'GITHUB_API_URL=https://api.github.com' \
  'GITHUB_REPOSITORY=blend/repo-that-uses-an-action' \
  "GITHUB_ACTOR=$(whoami)" \
  "GITHUB_WORKSPACE=$(pwd)/.." \
  "INPUT_TITLE=Start something exciting, dynamically..." \
  'INPUT_INTERACTIVE=fields:
  - label: overview
    properties:
      type: textarea
      description: Information on what this action does
      defaultValue: "This example is a powerful demonstration of how you can utilize the boasiHQ/interactive-inputs action to tailor the dynamic portal to your specific needs and desired output."
      readOnly: true
  - label: custom-file
    properties:
      display: Choose a file
      type: file
      description: Select the media you wish to send to the channel
      acceptedFileTypes:
        - image/png
        - video/mp4
  - label: custom-files
    properties:
      display: Choose at least one file or more
      required: true
      type: multifile
  - label: name
    properties:
      display: What is your name?
      type: text
      description: Name of the user
      maxLength: 20
      required: true
  - label: age
    properties:
      display: How old are you?
      type: number
      description: Age of the user
      placeholder: 18
      maxNumber: 20
      minNumber: 1
      required: false
  - label: city
    properties:
      display: What city do you live in?
      type: text
      description: City of the user
      maxLength: 20
      required: false 
  - label: car
    properties:
      display: Favourite Car
      type: select
      description: The name of your favourite car
      disableAutoCopySelection: false
      choices:
        - Ford
        - Toyota
        - Honda
        - Volvo
        - BMW
        - Mercedes
        - Audi
        - Lexus
        - Tesla
      required: true
  - label: colour
    properties:
      display: What are your favourite colours
      type: multiselect
      disableAutoCopySelection: true
      choices: 
        ["Red", "Green", "Blue", "Orange", "Purple", "Pink", "Yellow"]
      required: true
  - label: verify
    properties:
      display: Are you sure you want to continue?
      defaultValue: 'false'
      type: boolean
      required: true' \
  'INPUT_NOTIFIER-SLACK-ENABLED=false' \
  'INPUT_NOTIFIER-SLACK-TOKEN=xoxb-secret-token' \
  'INPUT_NOTIFIER-SLACK-CHANNEL=#random' \
  'INPUT_NOTIFIER-SLACK-BOT=' \
  'INPUT_NOTIFIER-SLACK-THREAD-TS=' \
  'INPUT_NOTIFIER-DISCORD-ENABLED=false' \
  'INPUT_NOTIFIER-DISCORD-WEBHOOK=secret-webhook' \
  'INPUT_NOTIFIER-DISCORD-USERNAME=' \
  'INPUT_NOTIFIER-DISCORD-THREAD-ID=' \
  'INPUT_GITHUB-TOKEN=github-secret-token' \
  'INPUT_NGROK-AUTHTOKEN=1234567890' \
  'IAIP_LOCAL_RUN=true' \
  'IAIP_SKIP_CONFIG_PARSE=0' \
  go run main.go