import Ember from 'ember';

export default Ember.Controller.extend({
  applicationController: Ember.inject.controller('application'),
  stats: Ember.computed.reads('applicationController.model.stats'),

  roundPercent: Ember.computed('stats.roundShares', 'model.roundShares', {
    get() {
      var roundShares = Number(this.getWithDefault('model.roundShares', 0));
      var totalRoundShares = Number(this.getWithDefault('stats.roundShares', 0));
      if (!roundShares || !totalRoundShares) {
        return 0;
      }
      return roundShares / totalRoundShares;
    }
  })
});
