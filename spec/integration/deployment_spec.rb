require 'spec_helper'

describe 'deployment integrations', type: :integration do
  with_reset_sandbox_before_each

  it 'updates job template accounting for deployment manifest properties' do
    manifest_hash = Bosh::Spec::Deployments.simple_manifest
    manifest_hash['properties'] = { 'test_property' => 1 }
    deploy_simple(manifest_hash: manifest_hash)

    agent_id = get_job_vm('foobar/0')[:agent_id]
    ctl_path = File.join(current_sandbox.agent_tmp_path, "agent-base-dir-#{agent_id}", 'jobs', 'foobar', 'bin', 'foobar_ctl')
    expect(File.read(ctl_path)).to include('test_property=1')

    manifest_hash['properties'] = { 'test_property' => 2 }
    deploy_simple_manifest(manifest_hash: manifest_hash)
    expect(File.read(ctl_path)).to include('test_property=2')
  end

  it 'updates a job with multiple instances in parallel and obey max_in_flight' do
    manifest_hash = Bosh::Spec::Deployments.simple_manifest
    manifest_hash['releases'].first['version'] = 'latest'
    manifest_hash['update']['canaries'] = 0
    manifest_hash['properties'] = { 'test_property' => 2 }
    manifest_hash['update']['max_in_flight'] = 2
    deploy_simple(manifest_hash: manifest_hash)

    times = start_and_finish_times_for_job_updates('last')
    expect(times['foobar/1']['started']).to be >= times['foobar/0']['started']
    expect(times['foobar/1']['started']).to be < times['foobar/0']['finished']
    expect(times['foobar/2']['started']).to be >= [times['foobar/0']['finished'], times['foobar/1']['finished']].min
  end

  it 'set resource pool size to auto' do
    manifest_hash = Bosh::Spec::Deployments.simple_manifest
    manifest_hash['releases'].first['version'] = 'latest'
    manifest_hash['update']['canaries'] = 0
    manifest_hash['properties'] = { 'test_property' => 2 }
    manifest_hash['update']['max_in_flight'] = 2
    output = deploy_simple(manifest_hash: manifest_hash)
    task_id = get_task_id(output)
    times = start_and_finish_times_for_job_updates('last')
    expect(times['foobar/1']['started']).to be >= times['foobar/0']['started']
    expect(times['foobar/1']['started']).to be < times['foobar/0']['finished']
    expect(times['foobar/2']['started']).to be >= [times['foobar/0']['finished'], times['foobar/1']['finished']].min
  end

  it 'spawns a job and then successfully cancel it' do
    deploy_result = deploy_simple(no_track: true)
    task_id = get_task_id(deploy_result, 'running')

    cancel_output = run_bosh("cancel task #{task_id}")
    expect($?).to be_success
    expect(cancel_output).to match /Task #{task_id} is getting canceled/

    error_event = events(task_id).last['error']
    expect(error_event['code']).to eq(10001)
    expect(error_event['message']).to eq("Task #{task_id} cancelled")
  end

  it 'does not finish a deployment if job update fails' do
    pending if current_sandbox.agent_type == "ruby"
    deploy_simple
    failing_agent_id = get_job_vm('foobar/0')[:agent_id]

    set_agent_job_state(failing_agent_id, "failing")

    manifest_hash = Bosh::Spec::Deployments.simple_manifest
    manifest_hash['update']['canary_watch_time'] = 0
    manifest_hash['jobs'][0]['instances'] = 2
    manifest_hash['resource_pools'][0]['size'] = 2

    set_deployment(manifest_hash: manifest_hash)
    deploy_result = deploy(failure_expected: true)
    expect($?).to_not be_success

    task_id = get_task_id(deploy_result, 'error')
    task_events = events(task_id)

    failing_job_event = task_events[-2]
    expect(failing_job_event['stage']).to eq('Updating job')
    expect(failing_job_event['state']).to eq('failed')
    expect(failing_job_event['task']).to eq('foobar/0 (canary)')

    started_job_events = task_events.select do |e|
      e['stage'] == 'Updating job' && e['state'] == "started"
    end

    expect(started_job_events.size).to eq(1)
  end

  def start_and_finish_times_for_job_updates(task_id)
    jobs = {}
    events(task_id).select do |e|
      e['stage'] == 'Updating job' && %w(started finished).include?(e['state'])
    end.each do |e|
      jobs[e['task']] ||= {}
      jobs[e['task']][e['state']] = e['time']
    end
    jobs
  end

  def events(task_id)
    result = run_bosh("task #{task_id} --raw")
    event_list = []
    result.each_line do |line|
      begin
        event = Yajl::Parser.new.parse(line)
        event_list << event if event
      rescue Yajl::ParseError
      end
    end
    event_list
  end

  def get_task_id(output, state = 'done')
    match = output.match(/Task (\d+) #{state}/)
    expect(match).to_not be(nil)
    match[1]
  end

  def set_agent_job_state(agent_id, state)
    NATS.start(uri: "nats://localhost:#{current_sandbox.nats_port}") do
      msg = Yajl::Encoder.encode(
        method: 'set_dummy_status',
        status: state,
        reply_to: 'integration.tests',
      )

      NATS.publish("agent.#{agent_id}", msg) { NATS.stop }
    end
  end
end
